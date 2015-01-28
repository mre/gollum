package producer

import (
	"gollum/shared"
	"os"
)

// File producer plugin
// Configuration example
//
// - "producer.File":
//   Enable: true
//   File: "/var/log/gollum.log"
//   BatchSize: 4096
//   BatchSizeThreshold: 16777216
//   BatchTimeoutSec: 2
//
// File contains the path to the log file to write.
// By default this is set to /var/prod/gollum.log.
//
// BatchSize defines the number of bytes to be buffered before they are written
// to disk. By default this is set to 8KB.
//
// BatchSizeThreshold defines the maximum number of bytes to buffer before
// messages get dropped. Any message that crosses the threshold is dropped.
// By default this is set to 8MB.
//
// BatchTimeoutSec defines the maximum number of seconds to wait after the last
// message arrived before a batch is flushed automatically. By default this is
// set to 5.
type File struct {
	standardProducer
	file            *os.File
	batch           *shared.MessageBuffer
	batchSize       int
	batchTimeoutSec int
}

func init() {
	shared.Plugin.Register(File{})
}

func (prod File) Create(conf shared.PluginConfig) (shared.Producer, error) {
	err := prod.configureStandardProducer(conf)
	if err != nil {
		return nil, err
	}

	logFile := conf.GetString("File", "/var/prod/gollum.log")
	batchSizeThreshold := conf.GetInt("BatchThreshold", 8388608)

	prod.batchSize = conf.GetInt("BatchSize", 8192)
	prod.file, err = os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	prod.batch = shared.CreateMessageBuffer(batchSizeThreshold)

	return prod, nil
}

func (prod File) write() {
	err := prod.batch.Flush(prod.file)
	if err != nil {
		shared.Log.Error("File error:", err)
	}
}

func (prod File) Produce() {
	defer func() {
		prod.file.Close()
		prod.response <- shared.ProducerControlResponseDone
	}()

	for {
		select {
		case message := <-prod.messages:
			prod.batch.AppendAndRelease(message, prod.forward)
			if prod.batch.ReachedSizeThreshold(prod.batchSize) {
				prod.write()
			}

		case command := <-prod.control:
			if command == shared.ProducerControlStop {
				return // ### return, done ###
			}
		default:
			if prod.batch.ReachedTimeThreshold(prod.batchTimeoutSec) {
				prod.write()
			}
			// Don't block
		}
	}
}
