package playback

// Audio-playback using mpv media-server. See mpv.io
// https://github.com/dexterlb/mpvipc
// https://mpv.io/manual/master/#json-ipc
// https://mpv.io/manual/master/#properties

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dexterlb/mpvipc"
	"github.com/navidrome/navidrome/core/playback/mpv"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
)

type SpeakerTrack interface {
	IsPlaying() bool
	SetVolume(value float32) // Used to control the playback volume. A float value between 0.0 and 1.0.
	Pause()
	Unpause()
	Position() int
	SetPosition(offset int) error
	Close()
	String() string
}

type SpeakerPlaybackDevice struct {
	serviceCtx           context.Context
	ParentPlaybackServer PlaybackServer
	MpvConn              *mpvipc.Connection
	Default              bool
	Events               mpvipc.Event
	Name                 string
	DeviceName           string
	PlaybackQueue        *Queue
	Gain                 float32
	PlaybackDone         chan bool
	startTrackSwitcher   sync.Once
}

func (pd *SpeakerPlaybackDevice) Position() int {
	retryCount := 0
	for {
		position, err := pd.MpvConn.Get("time-pos")
		if err != nil && err.Error() == "mpv error: property unavailable" {
			retryCount += 1
			log.Debug("Got mpv error, retrying...", "retries", retryCount, err)
			if retryCount > 5 {
				return 0
			}
			time.Sleep(time.Duration(retryCount) * time.Millisecond)
			continue
		}

		if err != nil {
			log.Error("Error getting position in track", "track", pd, err)
			return 0
		}

		pos, ok := position.(float64)
		if !ok {
			log.Error("Could not cast position from mpv into float64", "position", position, "track", pd)
			return 0
		} else {
			return int(pos)
		}
	}
}

func (pd *SpeakerPlaybackDevice) getStatus() DeviceStatus {
	return DeviceStatus{
		CurrentIndex: pd.PlaybackQueue.Index,
		Playing:      pd.isPlaying(),
		Gain:         pd.Gain,
		Position:     pd.Position(),
	}
}

// NewPlaybackDevice creates a new playback device which implements all the basic Jukebox mode commands defined here:
// http://www.subsonic.org/pages/api.jsp#jukeboxControl
// Starts the trackSwitcher goroutine for the device.
func NewSpeakerPlaybackDevice(ctx context.Context, playbackServer PlaybackServer, name string, deviceName string) *SpeakerPlaybackDevice {
	conn, err := mpv.OpenMpvAndConnection(ctx, deviceName)
	_ = err
	pd := &SpeakerPlaybackDevice{
		serviceCtx:           ctx,
		ParentPlaybackServer: playbackServer,
		Name:                 name,
		MpvConn:              conn,
		DeviceName:           deviceName,
		Gain:                 1.0,
		PlaybackQueue:        NewQueue(),
		PlaybackDone:         make(chan bool),
	}
	//pd.Events = make(chan mpvipc.Event)
	return pd
}

func (pd *SpeakerPlaybackDevice) String() string {
	return fmt.Sprintf("Name: %s, Gain: %.4f", pd.Name, pd.Gain)
}

func (pd *SpeakerPlaybackDevice) Get(ctx context.Context) (model.MediaFiles, DeviceStatus, error) {
	log.Debug(ctx, "Processing Get action", "device", pd)
	return pd.PlaybackQueue.Get(), pd.getStatus(), nil
}

func (pd *SpeakerPlaybackDevice) Status(ctx context.Context) (DeviceStatus, error) {
	log.Debug(ctx, fmt.Sprintf("processing Status action on: %s, queue: %s", pd, pd.PlaybackQueue))
	return pd.getStatus(), nil
}

// Set is similar to a clear followed by a add, but will not change the currently playing track.
func (pd *SpeakerPlaybackDevice) Set(ctx context.Context, ids []string) (DeviceStatus, error) {
	log.Debug(ctx, "Processing Set action", "ids", ids, "device", pd)

	_, err := pd.Clear(ctx)
	if err != nil {
		log.Error(ctx, "error setting tracks", ids)
		return pd.getStatus(), err
	}
	return pd.Add(ctx, ids)
}

func (pd *SpeakerPlaybackDevice) Start(ctx context.Context) (DeviceStatus, error) {
	log.Debug(ctx, "Processing Start action", "device", pd)

	pd.startTrackSwitcher.Do(func() {
		log.Info(ctx, "Starting trackSwitcher goroutine")
		// Start one trackSwitcher goroutine with each device
		go func() {
			pd.trackSwitcherGoroutine()
		}()
	})

	if !pd.PlaybackQueue.IsEmpty() {
		err := pd.switchActiveTrackByIndex(pd.PlaybackQueue.Index, 0)
		if err != nil {
			return pd.getStatus(), err
		}
		err = pd.MpvConn.Set("pause", false)
		if err != nil {
			log.Error("Error pausing track", "track", pd, err)
		}
	}

	return pd.getStatus(), nil
}

func (pd *SpeakerPlaybackDevice) Stop(ctx context.Context) (DeviceStatus, error) {
	log.Debug(ctx, "Processing Stop action", "device", pd)

	err := pd.MpvConn.Set("pause", true)
	if err != nil {
		log.Error("Error pausing track", "track", pd, err)
	}

	return pd.getStatus(), nil
}

func (pd *SpeakerPlaybackDevice) Skip(ctx context.Context, index int, offset int) (DeviceStatus, error) {
	log.Debug(ctx, "Processing Skip action", "index", index, "offset", offset, "device", pd)

	if index != pd.PlaybackQueue.Index {
		pd.switchActiveTrackByIndex(index, offset)
	} else {
		pd.MpvConn.Call("seek", offset)
	}

	return pd.getStatus(), nil
}

func (pd *SpeakerPlaybackDevice) Add(ctx context.Context, ids []string) (DeviceStatus, error) {
	log.Debug(ctx, "Processing Add action", "ids", ids, "device", pd)
	if len(ids) < 1 {
		return pd.getStatus(), nil
	}

	items := model.MediaFiles{}

	for _, id := range ids {
		mf, err := pd.ParentPlaybackServer.GetMediaFile(id)
		if err != nil {
			return DeviceStatus{}, err
		}
		log.Debug(ctx, "Found mediafile: "+mf.Path)
		items = append(items, *mf)
	}
	pd.PlaybackQueue.Add(items)

	return pd.getStatus(), nil
}

func (pd *SpeakerPlaybackDevice) Clear(ctx context.Context) (DeviceStatus, error) {
	log.Debug(ctx, "Processing Clear action", "device", pd)
	pd.Stop(ctx)
	pd.PlaybackQueue.Clear()
	return pd.getStatus(), nil
}

func (pd *SpeakerPlaybackDevice) Remove(ctx context.Context, index int) (DeviceStatus, error) {
	log.Debug(ctx, "Processing Remove action", "index", index, "device", pd)
	// pausing if attempting to remove running track
	if pd.isPlaying() && pd.PlaybackQueue.Index == index {
		_, err := pd.Stop(ctx)
		if err != nil {
			log.Error(ctx, "error stopping running track")
			return pd.getStatus(), err
		}
	}

	if index > -1 && index < pd.PlaybackQueue.Size() {
		pd.PlaybackQueue.Remove(index)
	} else {
		log.Error(ctx, "Index to remove out of range: "+fmt.Sprint(index))
	}
	return pd.getStatus(), nil
}

func (pd *SpeakerPlaybackDevice) Shuffle(ctx context.Context) (DeviceStatus, error) {
	log.Debug(ctx, "Processing Shuffle action", "device", pd)
	if pd.PlaybackQueue.Size() > 1 {
		pd.PlaybackQueue.Shuffle()
	}
	return pd.getStatus(), nil
}

// SetGain is used to control the playback volume. A float value between 0.0 and 1.0.
func (pd *SpeakerPlaybackDevice) SetGain(ctx context.Context, gain float32) (DeviceStatus, error) {
	log.Debug(ctx, "Processing SetGain action", "newGain", gain, "device", pd)

	vol := int(gain * 100)

	err := pd.MpvConn.Set("volume", vol)
	if err != nil {
		log.Error("Error setting volume", "volume", gain, "track", pd, err)
	}
	pd.Gain = gain

	return pd.getStatus(), nil
}

func (pd *SpeakerPlaybackDevice) isPlaying() bool {
	pausing, err := pd.MpvConn.Get("pause")
	if err != nil {
		log.Error("Problem getting paused status", "track", pd, err)
		return false
	}

	pause, ok := pausing.(bool)
	if !ok {
		log.Error("Could not cast pausing to boolean", "track", pd, "value", pausing)
		return false
	}
	return !pause
}

func (pd *SpeakerPlaybackDevice) trackSwitcherGoroutine() {
	log.Debug("Started trackSwitcher goroutine", "device", pd)
	for {
		select {
		case <-pd.PlaybackDone:
			//log.Debug("Track switching detected")
			//if pd.ActiveTrack != nil {
			//	pd.ActiveTrack.Close()
			//	pd.ActiveTrack = nil
			//}
			//
			//if !pd.PlaybackQueue.IsAtLastElement() {
			//	pd.PlaybackQueue.IncreaseIndex()
			//	log.Debug("Switching to next song", "queue", pd.PlaybackQueue.String())
			//	err := pd.switchActiveTrackByIndex(pd.PlaybackQueue.Index, 0)
			//	if err != nil {
			//		log.Error("Error switching track", err)
			//	}
			//	if pd.ActiveTrack != nil {
			//		pd.ActiveTrack.Unpause()
			//	}
			//} else {
			//	log.Debug("There is no song left in the playlist. Finish.")
			//}
		case <-pd.serviceCtx.Done():
			log.Debug("Stopping trackSwitcher goroutine", "device", pd.Name)
			return
		}
	}
}

func (pd *SpeakerPlaybackDevice) switchActiveTrackByIndex(index int, offset int) error {
	pd.PlaybackQueue.SetIndex(index)
	currentTrack := pd.PlaybackQueue.Current()
	if currentTrack == nil {
		return errors.New("could not get current track")
	}

	pd.MpvConn.Call("loadfile", currentTrack.Path, "replace", 0, "start=10")

	return nil
}
