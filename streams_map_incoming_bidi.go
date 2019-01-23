// This file was automatically generated by genny.
// Any changes will be lost if this file is regenerated.
// see https://github.com/cheekybits/genny

package quic

import (
	"fmt"
	"sync"

	"github.com/lucas-clemente/quic-go/internal/protocol"
	"github.com/lucas-clemente/quic-go/internal/wire"
)

type incomingBidiStreamsMap struct {
	mutex sync.RWMutex
	cond  sync.Cond

	streams map[protocol.StreamID]streamI
	// When a stream is deleted before it was accepted, we can't delete it immediately.
	// We need to wait until the application accepts it, and delete it immediately then.
	streamsToDelete map[protocol.StreamID]struct{} // used as a set

	nextStream    protocol.StreamID // the next stream that will be returned by AcceptStream()
	highestStream protocol.StreamID // the highest stream that the peer openend
	maxStream     protocol.StreamID // the highest stream that the peer is allowed to open
	maxNumStreams int               // maximum number of streams

	newStream        func(protocol.StreamID) streamI
	queueMaxStreamID func(*wire.MaxStreamIDFrame)

	closeErr error
}

func newIncomingBidiStreamsMap(
	nextStream protocol.StreamID,
	initialMaxStreamID protocol.StreamID,
	maxNumStreams int,
	queueControlFrame func(wire.Frame),
	newStream func(protocol.StreamID) streamI,
) *incomingBidiStreamsMap {
	m := &incomingBidiStreamsMap{
		streams:          make(map[protocol.StreamID]streamI),
		streamsToDelete:  make(map[protocol.StreamID]struct{}),
		nextStream:       nextStream,
		maxStream:        initialMaxStreamID,
		maxNumStreams:    maxNumStreams,
		newStream:        newStream,
		queueMaxStreamID: func(f *wire.MaxStreamIDFrame) { queueControlFrame(f) },
	}
	m.cond.L = &m.mutex
	return m
}

func (m *incomingBidiStreamsMap) AcceptStream() (streamI, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	var id protocol.StreamID
	var str streamI
	for {
		id = m.nextStream
		var ok bool
		if m.closeErr != nil {
			return nil, m.closeErr
		}
		str, ok = m.streams[id]
		if ok {
			break
		}
		m.cond.Wait()
	}
	m.nextStream += 4
	// If this stream was completed before being accepted, we can delete it now.
	if _, ok := m.streamsToDelete[id]; ok {
		delete(m.streamsToDelete, id)
		if err := m.deleteStream(id); err != nil {
			return nil, err
		}
	}
	return str, nil
}

func (m *incomingBidiStreamsMap) GetOrOpenStream(id protocol.StreamID) (streamI, error) {
	m.mutex.RLock()
	if id > m.maxStream {
		m.mutex.RUnlock()
		return nil, fmt.Errorf("peer tried to open stream %d (current limit: %d)", id, m.maxStream)
	}
	// if the id is smaller than the highest we accepted
	// * this stream exists in the map, and we can return it, or
	// * this stream was already closed, then we can return the nil
	if id <= m.highestStream {
		var s streamI
		// If the stream was already queued for deletion, and is just waiting to be accepted, don't return it.
		if _, ok := m.streamsToDelete[id]; !ok {
			s = m.streams[id]
		}
		m.mutex.RUnlock()
		return s, nil
	}
	m.mutex.RUnlock()

	m.mutex.Lock()
	// no need to check the two error conditions from above again
	// * maxStream can only increase, so if the id was valid before, it definitely is valid now
	// * highestStream is only modified by this function
	var start protocol.StreamID
	if m.highestStream == 0 {
		start = m.nextStream
	} else {
		start = m.highestStream + 4
	}
	for newID := start; newID <= id; newID += 4 {
		m.streams[newID] = m.newStream(newID)
		m.cond.Signal()
	}
	m.highestStream = id
	s := m.streams[id]
	m.mutex.Unlock()
	return s, nil
}

func (m *incomingBidiStreamsMap) DeleteStream(id protocol.StreamID) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	return m.deleteStream(id)
}

func (m *incomingBidiStreamsMap) deleteStream(id protocol.StreamID) error {
	if _, ok := m.streams[id]; !ok {
		return fmt.Errorf("Tried to delete unknown stream %d", id)
	}

	// Don't delete this stream yet, if it was not yet accepted.
	// Just save it to streamsToDelete map, to make sure it is deleted as soon as it gets accepted.
	if id >= m.nextStream {
		if _, ok := m.streamsToDelete[id]; ok {
			return fmt.Errorf("Tried to delete stream %d multiple times", id)
		}
		m.streamsToDelete[id] = struct{}{}
		return nil
	}

	delete(m.streams, id)
	// queue a MAX_STREAM_ID frame, giving the peer the option to open a new stream
	if numNewStreams := m.maxNumStreams - len(m.streams); numNewStreams > 0 {
		m.maxStream = m.highestStream + protocol.StreamID(numNewStreams*4)
		m.queueMaxStreamID(&wire.MaxStreamIDFrame{StreamID: m.maxStream})
	}
	return nil
}

func (m *incomingBidiStreamsMap) CloseWithError(err error) {
	m.mutex.Lock()
	m.closeErr = err
	for _, str := range m.streams {
		str.closeForShutdown(err)
	}
	m.mutex.Unlock()
	m.cond.Broadcast()
}
