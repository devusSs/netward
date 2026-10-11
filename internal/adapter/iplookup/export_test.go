package iplookup

func IsUnavailable(err error) bool {
	return isUnavailable(err)
}
