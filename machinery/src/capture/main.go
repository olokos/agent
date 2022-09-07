// Connecting to different camera sources and make it recording to disk.
package capture

import (
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/kerberos-io/agent/machinery/src/log"
	"github.com/kerberos-io/agent/machinery/src/models"
	"github.com/kerberos-io/agent/machinery/src/utils"
	"github.com/kerberos-io/joy4/av/pubsub"
	"github.com/kerberos-io/joy4/format/mp4"

	"github.com/kerberos-io/joy4/av"
)

func CleanupRecordingDirectory(configuration *models.Configuration) {
	autoClean := configuration.Config.AutoClean
	if autoClean == "true" {
		maxSize := configuration.Config.MaxDirectorySize
		if maxSize == 0 {
			maxSize = 50
		}
		// Total size of the recording directory.
		recordingsDirectory := "./data/recordings"
		size, err := utils.DirSize(recordingsDirectory)
		if err == nil {
			sizeInMB := size / 1000 / 1000
			if sizeInMB >= maxSize {
				// Remove the oldest recording
				oldestFile, err := utils.FindOldestFile(recordingsDirectory)
				if err == nil {
					err := os.Remove(recordingsDirectory + "/" + oldestFile.Name())
					log.Log.Info("HandleRecordStream: removed oldest file as part of cleanup - " + recordingsDirectory + "/" + oldestFile.Name())
					if err != nil {
						log.Log.Info("HandleRecordStream: something went wrong, " + err.Error())
					}
				} else {
					log.Log.Info("HandleRecordStream: something went wrong, " + err.Error())
				}
			}
		} else {
			log.Log.Info("HandleRecordStream: something went wrong, " + err.Error())
		}

	} else {
		log.Log.Info("HandleRecordStream: Autoclean disabled, nothing to do here.")
	}
}

func HandleRecordStream(recordingCursor *pubsub.QueueCursor, configuration *models.Configuration, communication *models.Communication, streams []av.CodecData) {
	log.Log.Debug("HandleRecordStream: started")

	config := configuration.Config
	recordingPeriod := config.Capture.PostRecording         // number of seconds to record.
	maxRecordingPeriod := config.Capture.MaxLengthRecording // maximum number of seconds to record.

	// Synchronise the last synced time
	now := int(time.Now().Unix())
	startRecording := int(now)
	timestamp := int(now)

	// Check if continuous recording.
	if config.Capture.Continuous == "true" {

		// Do not do anything!
		log.Log.Info("HandleRecordStream: Start continuous recording ")

		loc, _ := time.LoadLocation(config.Timezone)
		now = int(time.Now().Unix())
		timestamp = int(now)
		start := false
		var name string
		var myMuxer *mp4.Muxer
		var file *os.File
		var err error

		// If continuous record the full length
		recordingPeriod = maxRecordingPeriod
		// Recording file name
		fullName := ""

		// Get as much packets we need.
		//for pkt := range packets {
		var cursorError error
		var pkt av.Packet
		recordingStatus := "idle"

		for cursorError == nil {

			pkt, cursorError = recordingCursor.ReadPacket()

			now := int(time.Now().Unix())

			if start && // If already recording and current frame is a keyframe and we should stop recording
				pkt.IsKeyFrame && (timestamp+recordingPeriod-now <= 0 || now-startRecording >= maxRecordingPeriod) {

				// This will write the trailer a well.
				if err := myMuxer.WriteTrailer(); err != nil {
					log.Log.Error(err.Error())
				}

				log.Log.Info("HandleRecordStream: Recording finished: file save: " + name)
				file.Close()

				// Check if need to convert to fragmented using bento
				if config.Capture.Fragmented == "true" && config.Capture.FragmentedDuration > 0 {
					utils.CreateFragmentedMP4(fullName, config.Capture.FragmentedDuration)
				}

				// Create a symbol link.
				fc, _ := os.Create("./data/cloud/" + name)
				fc.Close()

				// Cleanup muxer
				start = false
				myMuxer = nil
				runtime.GC()
				debug.FreeOSMemory()

				recordingStatus = "idle"

				// Clean up the recording directory if necessary.
				CleanupRecordingDirectory(configuration)
			}

			// If not yet started and a keyframe, let's make a recording
			if !start && pkt.IsKeyFrame {

				// Check if within time interval
				nowInTimezone := time.Now().In(loc)
				weekday := nowInTimezone.Weekday()
				hour := nowInTimezone.Hour()
				minute := nowInTimezone.Minute()
				second := nowInTimezone.Second()
				timeEnabled := config.Time
				timeInterval := config.Timetable[int(weekday)]

				if timeEnabled == "true" && timeInterval != nil {
					start1 := timeInterval.Start1
					end1 := timeInterval.End1
					start2 := timeInterval.Start2
					end2 := timeInterval.End2
					currentTimeInSeconds := hour*60*60 + minute*60 + second
					if (currentTimeInSeconds >= start1 && currentTimeInSeconds <= end1) ||
						(currentTimeInSeconds >= start2 && currentTimeInSeconds <= end2) {

					} else {
						log.Log.Debug("HandleRecordStream: Disabled: no continuous recording at this moment. Not within specified time interval.")
						time.Sleep(5 * time.Second)
						continue
					}
				}

				start = true
				timestamp = now

				// timestamp_microseconds_instanceName_regionCoordinates_numberOfChanges_token
				// 1564859471_6-474162_oprit_577-283-727-375_1153_27.mp4
				// - Timestamp
				// - Size + - + microseconds
				// - device
				// - Region
				// - Number of changes
				// - Token

				startRecording = int(time.Now().Unix()) // we mark the current time when the record started.ss
				s := strconv.Itoa(startRecording) + "_" +
					"6" + "-" +
					"967003" + "_" +
					config.Name + "_" +
					"200-200-400-400" + "_0_" +
					"769"

				name = s + ".mp4"
				fullName = "./data/recordings/" + name

				// Running...
				log.Log.Info("Recording started")

				file, err = os.Create(fullName)
				if err == nil {
					myMuxer = mp4.NewMuxer(file)
				}

				log.Log.Info("HandleRecordStream: composing recording")
				log.Log.Info("HandleRecordStream: write header")

				// Creating the file, might block sometimes.
				if err := myMuxer.WriteHeader(streams); err != nil {
					log.Log.Error(err.Error())
				}

				if err := myMuxer.WritePacket(pkt); err != nil {
					log.Log.Error(err.Error())
				}

				recordingStatus = "started"

			} else if start {
				if err := myMuxer.WritePacket(pkt); err != nil {
					log.Log.Error(err.Error())
				}
			}
		}

		// We might have interrupted the recording while restarting the agent.
		// If this happens we need to check to properly close the recording.
		if cursorError != nil {
			if recordingStatus == "started" {
				// This will write the trailer a well.
				if err := myMuxer.WriteTrailer(); err != nil {
					log.Log.Error(err.Error())
				}

				log.Log.Info("HandleRecordStream: Recording finished: file save: " + name)
				file.Close()

				// Check if need to convert to fragmented using bento
				if config.Capture.Fragmented == "true" && config.Capture.FragmentedDuration > 0 {
					utils.CreateFragmentedMP4(fullName, config.Capture.FragmentedDuration)
				}

				// Create a symbol link.
				fc, _ := os.Create("./data/cloud/" + name)
				fc.Close()

				// Cleanup muxer
				start = false
				myMuxer = nil
				runtime.GC()
				debug.FreeOSMemory()

				recordingStatus = "idle"
			}
		}
	} else {

		log.Log.Info("HandleRecordStream: Start motion based recording ")

		var myMuxer *mp4.Muxer
		var file *os.File
		var err error

		for motion := range communication.HandleMotion {

			timestamp = int(time.Now().Unix())
			startRecording = int(time.Now().Unix()) // we mark the current time when the record started.
			numberOfChanges := motion.NumberOfChanges

			// timestamp_microseconds_instanceName_regionCoordinates_numberOfChanges_token
			// 1564859471_6-474162_oprit_577-283-727-375_1153_27.mp4
			// - Timestamp
			// - Size + - + microseconds
			// - device
			// - Region
			// - Number of changes
			// - Token

			s := strconv.Itoa(startRecording) + "_" +
				"6" + "-" +
				"967003" + "_" +
				config.Name + "_" +
				"200-200-400-400" + "_" +
				strconv.Itoa(numberOfChanges) + "_" +
				"769"

			name := s + ".mp4"
			fullName := "./data/recordings/" + name

			// Running...
			log.Log.Info("HandleRecordStream: Recording started")
			file, err = os.Create(fullName)
			if err == nil {
				myMuxer = mp4.NewMuxer(file)
			}

			start := false

			log.Log.Info("HandleRecordStream: composing recording")
			log.Log.Info("HandleRecordStream: write header")
			// Creating the file, might block sometimes.
			if err := myMuxer.WriteHeader(streams); err != nil {
				log.Log.Error(err.Error())
			}

			// Get as much packets we need.
			//for pkt := range packets {

			var cursorError error
			var pkt av.Packet

			for cursorError == nil {

				pkt, cursorError = recordingCursor.ReadPacket()
				if cursorError != nil {
					log.Log.Error("HandleRecordStream: " + cursorError.Error())
				}

				now := int(time.Now().Unix())
				select {
				case motion := <-communication.HandleMotion:
					timestamp = int(now)
					log.Log.Info("HandleRecordStream: motion detected while recording. Expanding recording.")
					numberOfChanges = motion.NumberOfChanges
					log.Log.Info("Received message with recording data, detected changes to save: " + strconv.Itoa(numberOfChanges))
				default:
				}

				if timestamp+recordingPeriod-now < 0 || now-startRecording > maxRecordingPeriod {
					log.Log.Info("HandleRecordStream: closing recording (timestamp: " + strconv.Itoa(timestamp) + ", recordingPeriod: " + strconv.Itoa(recordingPeriod) + ", now: " + strconv.Itoa(now) + ", startRecording: " + strconv.Itoa(startRecording) + ", maxRecordingPeriod: " + strconv.Itoa(maxRecordingPeriod))
					break
				}
				if pkt.IsKeyFrame && start == false {
					log.Log.Info("HandleRecordStream: write frames")
					start = true
				}
				if start {
					if err := myMuxer.WritePacket(pkt); err != nil {
						log.Log.Error(err.Error())
					}
				}
			}

			// This will write the trailer as well.
			myMuxer.WriteTrailer()
			log.Log.Info("HandleRecordStream:  file save: " + name)
			file.Close()
			myMuxer = nil
			runtime.GC()
			debug.FreeOSMemory()

			// Check if need to convert to fragmented using bento
			if config.Capture.Fragmented == "true" && config.Capture.FragmentedDuration > 0 {
				utils.CreateFragmentedMP4(fullName, config.Capture.FragmentedDuration)
			}

			// Create a symbol linc.
			fc, _ := os.Create("./data/cloud/" + name)
			fc.Close()

			// Clean up the recording directory if necessary.
			CleanupRecordingDirectory(configuration)
		}
	}

	log.Log.Debug("HandleRecordStream: finished")
}
