package main

import (
	"os"
	"testing"

	"github.com/jssblck/akari/internal/config"
)

func TestMain(m *testing.M) {
	// Cloud and eph shells export AKARI_URL for the running server. LoadClient
	// honors it, so clear both credential vars and let each test set its own.
	_ = os.Unsetenv(config.URLEnvVar)
	_ = os.Unsetenv(config.TokenEnvVar)
	os.Exit(m.Run())
}
