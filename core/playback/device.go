package playback

import (
	"context"

	"github.com/navidrome/navidrome/model"
)

type PlaybackDevice interface {
	//get, status, set, start, stop, skip, add, clear, remove, shuffle, setGain
	Status(context.Context) (DeviceStatus, error)
	Get(context.Context) (model.MediaFiles, DeviceStatus, error)
	Set(context.Context, []string) (DeviceStatus, error)
	Start(context.Context) (DeviceStatus, error)
	Stop(context.Context) (DeviceStatus, error)
	Skip(context.Context, int, int) (DeviceStatus, error)
	Add(context.Context, []string) (DeviceStatus, error)
	Clear(context.Context) (DeviceStatus, error)
	Remove(context.Context, int) (DeviceStatus, error)
	Shuffle(context.Context) (DeviceStatus, error)
	SetGain(context.Context, float32) (DeviceStatus, error)
}

type DeviceStatus struct {
	CurrentIndex int
	Playing      bool
	Gain         float32
	Position     int
}
