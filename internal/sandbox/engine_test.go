package sandbox

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func frame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

func TestDemux(t *testing.T) {
	stream := bytes.Join([][]byte{frame(1, "out1 "), frame(2, "err "), frame(1, "out2")}, nil)
	tests := []struct {
		name      string
		max       int
		want      string
		truncated bool
	}{
		{"whole stream", 100, "out1 err out2", false},
		{"exact fit", 13, "out1 err out2", false},
		{"cut inside a frame", 7, "out1 er", true},
		{"cut at a frame boundary", 5, "out1 ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, truncated, err := demux(bytes.NewReader(stream), tt.max)
			if err != nil || string(got) != tt.want || truncated != tt.truncated {
				t.Fatalf("demux() = %q, %v, %v; want %q, %v", got, truncated, err, tt.want, tt.truncated)
			}
		})
	}
	if _, _, err := demux(bytes.NewReader(frame(1, "abc")[:9]), 100); err == nil {
		t.Error("demux accepted a short frame")
	}
}
