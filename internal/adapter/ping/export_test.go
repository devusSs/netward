package ping

import "time"

func IsUnavailable(err error) bool {
	return isUnavailable(err)
}

func IsNotPermitted(err error) bool {
	return isNotPermitted(err)
}

func Jitter(rtts []time.Duration) time.Duration {
	return jitter(rtts)
}
