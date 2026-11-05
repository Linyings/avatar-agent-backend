package providers

import (
	"encoding/binary"
	"testing"
)

func dubbingTestWAV(seconds int) []byte {
	data := make([]byte, 44+seconds*32000)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 16000)
	binary.LittleEndian.PutUint32(data[28:], 32000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(len(data)-44))
	return data
}
func TestDubbingDurationRejectsSpoofedAndOversizeWAV(t *testing.T) {
	good := dubbingTestWAV(3)
	if duration, err := DubbingWAVDuration(good); err != nil || duration != 3 {
		t.Fatalf("duration=%v err=%v", duration, err)
	}
	for _, mutation := range []func([]byte){
		func(b []byte) { binary.LittleEndian.PutUint32(b[28:], 64000) },
		func(b []byte) { binary.LittleEndian.PutUint32(b[40:], 32) },
		func(b []byte) { binary.LittleEndian.PutUint16(b[20:], 3) },
		func(b []byte) { binary.LittleEndian.PutUint32(b[4:], 0) },
	} {
		candidate := append([]byte(nil), good...)
		mutation(candidate)
		if _, err := DubbingWAVDuration(candidate); err == nil {
			t.Fatal("accepted malformed WAV")
		}
	}
	if _, err := DubbingWAVDuration(dubbingTestWAV(301)); err == nil {
		t.Fatal("accepted >5min")
	}
}

func TestDubbingRPCEncoding(t *testing.T) {
	if got := aliyunEscape("a b/+~"); got != "a%20b%2F%2B~" {
		t.Fatal(got)
	}
}
