package service

import "strconv"

func StringToFloat(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}

func StringToInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}
