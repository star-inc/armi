package rabbitmq

import "errors"

// errChannelUnavailable is returned when the AMQP channel is nil.
var errChannelUnavailable = errors.New("rabbitmq channel is unavailable")
