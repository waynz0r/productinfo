package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

var stdin *os.File

func Test_processFlags(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		check func(val interface{})
	}{
		{
			name: "flag made available through viper",
			args: []string{
				"--log-level", "debug",
			},
			check: func(val interface{}) {
				assert.Equal(t, "debug", val)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pflag.CommandLine = pflag.NewFlagSet(os.Args[0], pflag.ContinueOnError)
			// define flags
			defineFlags()
			// mock the input
			setupInputs(test.args, nil)
			test.check(viper.GetString("log-level"))

		})
	}
}

func Test_processProviderFlag(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		check func(val interface{})
	}{
		{
			name: "--provider flag properly made available through viper",
			args: []string{
				// notice the 3 ways providers may be given
				"--provider=ec2,gke", "--provider=azure", "--provider", "alibaba",
			},
			check: func(val interface{}) {
				assert.Equal(t, []string{"ec2", "gke", "azure", "alibaba"}, val)

			},
		},
		{
			name: "--provider flag default values",
			args: []string{
				// no provider flag specified
			},
			check: func(val interface{}) {
				assert.Equal(t, []string{Ec2, Gce, Azure, Oracle}, val)

			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pflag.CommandLine = pflag.NewFlagSet(os.Args[0], pflag.ContinueOnError)
			// define flags
			defineFlags()
			// mock the input
			setupInputs(test.args, nil)
			test.check(viper.GetStringSlice("provider"))

		})
	}
}

// setupInputs mocks out the command line argument list
func setupInputs(args []string, file *os.File) {

	// This trick allows command line flags to be be set in unit tests.
	// See https://github.com/VonC/gogitolite/commit/f656a9858cb7e948213db9564a9120097252b429
	a := os.Args[1:]
	if args != nil {
		a = args
	}
	pflag.CommandLine.Parse(a)
	viper.BindPFlags(pflag.CommandLine)

	// This enables stdin to be mocked for testing.
	if file != nil {
		stdin = file
	} else {
		stdin = os.Stdin
	}
}

func Test_configurationStringDefaults(t *testing.T) {
	tests := []struct {
		name     string
		viperKey string
		args     []string
		valType  interface{}
		check    func(val interface{})
	}{
		{
			name:     fmt.Sprintf("defaults for: %s", logLevelFlag),
			viperKey: logLevelFlag,
			args:     []string{}, // no flags provided
			valType:  "",
			check: func(val interface{}) {
				assert.Equal(t, "info", val, fmt.Sprintf("invalid default for %s", logLevelFlag))
			},
		},
		{
			name:     fmt.Sprintf("defaults for: %s", listenAddressFlag),
			viperKey: listenAddressFlag,
			args:     []string{}, // no flags provided
			check: func(val interface{}) {
				assert.Equal(t, ":9090", val, fmt.Sprintf("invalid default for %s", listenAddressFlag))
			},
		},
		{
			name:     fmt.Sprintf("defaults for: %s", prometheusAddressFlag),
			viperKey: prometheusAddressFlag,
			args:     []string{}, // no flags provided
			check: func(val interface{}) {
				assert.Equal(t, "", val, fmt.Sprintf("invalid default for %s", prometheusAddressFlag))
			},
		},
		{
			name:     fmt.Sprintf("defaults for: %s", prometheusQueryFlag),
			viperKey: prometheusQueryFlag,
			args:     []string{}, // no flags provided
			check: func(val interface{}) {
				assert.Equal(t, "avg_over_time(aws_spot_current_price{region=\"%s\", product_description=\"Linux/UNIX\"}[1w])", val, fmt.Sprintf("invalid default for %s", prometheusQueryFlag))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// cleaning flags
			pflag.CommandLine = pflag.NewFlagSet(os.Args[0], pflag.ContinueOnError)
			// define flags
			defineFlags()
			// mock the input
			setupInputs(test.args, nil)

			test.check(viper.Get(test.viperKey))
		})
	}
}
