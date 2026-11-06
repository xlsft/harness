//go:build !darwin

package sandbox

func darwinUserCacheDir() string { return "" }
