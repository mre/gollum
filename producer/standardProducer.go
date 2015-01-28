package producer

import (
	"gollum/shared"
	"regexp"
)

// Producer base class
// All producers support a common subset of configuration options:
//
// - "producer.Something":
//   Enable: true
//   Buffer: 1024
//   Forward: false
//   Stream:
//      - "error"
//      - "default"
//
// Enable switches the consumer on or off. By default this value is set to true.
// Buffer set the size of the channel used to communicate messages. By default
// this value is set to 1024.
// Stream contains either a single string or a list of strings defining the
// message channels this producer will consume. By default this is set to "*"
// which means "all streams".
// If forward is set to true, the message will be passed as-is, so date and
// channel will not be added. The default value is false.
type standardProducer struct {
	messages chan shared.Message
	control  chan int
	response chan int
	filter   *regexp.Regexp
	forward  bool
}

func (prod *standardProducer) configureStandardProducer(conf shared.PluginConfig) error {

	prod.messages = make(chan shared.Message, conf.Channel)
	prod.control = make(chan int, 1)
	prod.response = make(chan int, 1)
	prod.forward = conf.GetBool("Forward", false)
	prod.filter = nil

	filter := conf.GetString("Filter", "")

	if filter != "" {
		var err error
		prod.filter, err = regexp.Compile(filter)
		if err != nil {
			shared.Log.Error("Regex error: ", err)
		}
	}

	return nil
}

func (prod standardProducer) Accepts(message shared.Message) bool {
	if prod.filter == nil {
		return true // ### return, pass everything ###
	}

	return prod.filter.MatchString(string(message.Data.Buffer[:message.Data.Length]))
}

func (prod standardProducer) Control() chan<- int {
	return prod.control
}

func (prod standardProducer) ControlResponse() <-chan int {
	return prod.response
}

func (prod standardProducer) Messages() chan<- shared.Message {
	return prod.messages
}
