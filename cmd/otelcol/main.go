// Copyright Splunk, Inc.
// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Program otelcol is the OpenTelemetry Collector that collects stats
// and traces and exports to a configured backend.
package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/service"
	"go.opentelemetry.io/collector/service/parserprovider"
	"go.uber.org/zap"

	"github.com/signalfx/splunk-otel-collector/internal/components"
	"github.com/signalfx/splunk-otel-collector/internal/configprovider"
	"github.com/signalfx/splunk-otel-collector/internal/configsources"
	"github.com/signalfx/splunk-otel-collector/internal/version"
)

const (
	ballastEnvVarName     = "SPLUNK_BALLAST_SIZE_MIB"
	configEnvVarName      = "SPLUNK_CONFIG"
	configYamlEnvVarName  = "SPLUNK_CONFIG_YAML"
	memLimitMiBEnvVarName = "SPLUNK_MEMORY_LIMIT_MIB"
	memTotalEnvVarName    = "SPLUNK_MEMORY_TOTAL_MIB"
	realmEnvVarName       = "SPLUNK_REALM"
	tokenEnvVarName       = "SPLUNK_ACCESS_TOKEN"

	defaultDockerSAPMConfig        = "/etc/otel/collector/gateway_config.yaml"
	defaultDockerOTLPConfig        = "/etc/otel/collector/otlp_config_linux.yaml"
	defaultLocalSAPMConfig         = "cmd/otelcol/config/collector/gateway_config.yaml"
	defaultLocalOTLPConfig         = "cmd/otelcol/config/collector/otlp_config_linux.yaml"
	defaultMemoryBallastPercentage = 33
	defaultMemoryLimitPercentage   = 90
	defaultMemoryLimitMaxMiB       = 2048
	defaultMemoryTotalMiB          = 512
)

func main() {
	// TODO: Use same format as the collector
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	if !contains(os.Args[1:], "-h") && !contains(os.Args[1:], "--help") {
		checkRuntimeParams()
	}

	factories, err := components.Get()
	if err != nil {
		log.Fatalf("failed to build default components: %v", err)
	}

	info := component.BuildInfo{
		Command: "otelcol",
		Version: version.Version,
	}

	baseParserProvider := parserprovider.Default()
	if configYAML := os.Getenv(configYamlEnvVarName); configYAML != "" && os.Getenv(configEnvVarName) == "" {
		baseParserProvider = parserprovider.NewInMemory(bytes.NewBufferString(configYAML))
	}

	parserProvider := configprovider.NewConfigSourceParserProvider(
		baseParserProvider,
		zap.NewNop(), // The service logger is not available yet, setting it to NoP.
		info,
		configsources.Get()...,
	)

	serviceParams := service.AppSettings{
		BuildInfo:      info,
		Factories:      factories,
		ParserProvider: parserProvider,
	}

	if err := run(serviceParams); err != nil {
		log.Fatal(err)
	}
}

// Check whether a string exists in an array of CLI arguments
// Support key/value with and without an equal sign
func contains(arr []string, str string) bool {
	for _, a := range arr {
		// Command line argument may be of form
		// --key value OR --key=value
		if a == str {
			return true
		} else if strings.Contains(a, str+"=") {
			return true
		}
	}
	return false
}

// Get the value of a key in an array
// Support key/value with and with an equal sign
func getKeyValue(args []string, argName string) string {
	val := ""
	for i, arg := range args {
		switch {
		case strings.HasPrefix(arg, argName+"="):
			s := strings.Split(arg, "=")
			val = s[1]
		case arg == argName:
			i++
			val = args[i]
		}
	}
	return val
}

// Check runtime parameters
// Runtime parameters take priority over environment variables
// Config and ballast flags are checked
// Config and all memory env vars are checked
func checkRuntimeParams() {
	setConfigSource()

	// Set default total memory
	memTotalSizeMiB := defaultMemoryTotalMiB
	// Check if the total memory is specified via the env var
	memTotalEnvVarVal := os.Getenv(memTotalEnvVarName)
	// If so, validate and change total memory
	if memTotalEnvVarVal != "" {
		// Check if it is a numeric value.
		val, err := strconv.Atoi(memTotalEnvVarVal)
		if err != nil {
			log.Fatalf("Expected a number in %s env variable but got %s", memTotalEnvVarName, memTotalEnvVarVal)
		}
		// Ensure number is above some threshold
		if 99 > val {
			log.Fatalf("Expected a number greater than 99 for %s env variable but got %s", memTotalEnvVarName, memTotalEnvVarVal)
		}
		memTotalSizeMiB = val
	}

	// Check if memory ballast flag was passed
	// If so, ensure memory ballast env var is not set
	// Then set memory ballast and limit properly
	ballastSize := getKeyValue(os.Args[1:], "--mem-ballast-size-mib")
	if ballastSize != "" {
		if os.Getenv(ballastEnvVarName) != "" {
			log.Fatalf("Both %v and '--config' were specified, but only one is allowed", ballastEnvVarName)
		}
		os.Setenv(ballastEnvVarName, ballastSize)
	}
	setMemoryBallast(memTotalSizeMiB)
	setMemoryLimit(memTotalSizeMiB)
}

// Validate and equate specified config file path flag to the config file path env var
func setConfigSource() {
	// Config file path from cmd flag --config.
	pathFlag := getKeyValue(os.Args[1:], "--config")
	// Config file path from env var.
	pathVar := os.Getenv(configEnvVarName)
	// Config YAML from env var.
	yamlVar := os.Getenv(configYamlEnvVarName)

	// Restricting specifying config file path and config YAML env vars simultaneously.
	if pathVar != "" && yamlVar != "" {
		log.Fatalf("Specifying env vars %s and %s simultaneously is not allowed", configEnvVarName, configYamlEnvVarName)
	}

	if pathFlag == "" && yamlVar != "" {
		log.Printf("Configuring collector using YAML from env var %s", configYamlEnvVarName)
		return
	}

	// Config file path flag `--config` should take priority when running from most contexts.
	if pathFlag != "" {
		// Config file path flag takes precedence over config YAML env var.
		if yamlVar != "" {
			log.Printf("Both %v and '--config' were specified. Ignoring %q environment variable value and using configuration in %q", configYamlEnvVarName, yamlVar, pathFlag)
		}
		// Config file path flag takes precedence over config file path env var.
		if pathVar != "" && pathVar != pathFlag {
			log.Printf("Both %v and '--config' were specified. Overriding %q environment variable value with %q for this session", configEnvVarName, pathVar, pathFlag)
		}
		// Setting the config file path env var to the config file path flag value.
		pathVar = pathFlag
		os.Setenv(configEnvVarName, pathVar)
	}

	// Use a default config if no config given; supports Docker and local
	if pathVar == "" {
		_, err := os.Stat(defaultDockerSAPMConfig)
		if err == nil {
			pathVar = defaultDockerSAPMConfig
		}
		_, err = os.Stat(defaultLocalSAPMConfig)
		if err == nil {
			pathVar = defaultLocalSAPMConfig
		}
		if pathVar == "" {
			log.Fatalf("Unable to find the default configuration file, ensure %s environment variable is set properly", configEnvVarName)
		}
	} else {
		// Check if file exists.
		_, err := os.Stat(pathVar)
		if err != nil {
			log.Fatalf("Unable to find the configuration file (%s) ensure %s environment variable is set properly", pathVar, configEnvVarName)
		}
	}

	switch pathVar {
	case
		defaultDockerSAPMConfig,
		defaultDockerOTLPConfig,
		defaultLocalSAPMConfig,
		defaultLocalOTLPConfig:
		// The following environment variables are required.
		// If any are missing stop here.
		requiredEnvVars := []string{realmEnvVarName, tokenEnvVarName}
		for _, v := range requiredEnvVars {
			if len(os.Getenv(v)) == 0 {
				log.Printf("Usage: %s=12345 %s=us0 %s", tokenEnvVarName, realmEnvVarName, os.Args[0])
				log.Fatalf("ERROR: Missing required environment variable %s with default config path %s", v, pathVar)
			}
		}
	}

	if !contains(os.Args[1:], "--config") {
		// Inject the command line flag that controls the configuration.
		os.Args = append(os.Args, "--config="+pathVar)
	}
	log.Printf("Set config to %v", pathVar)
}

// Validate and set the memory ballast
func setMemoryBallast(memTotalSizeMiB int) {
	// Check if the memory ballast is specified via the env var
	ballastSize := os.Getenv(ballastEnvVarName)
	// If so, validate and set properly
	if ballastSize != "" {
		// Check if it is a numeric value.
		val, err := strconv.Atoi(ballastSize)
		if err != nil {
			log.Fatalf("Expected a number in %s env variable but got %s", ballastEnvVarName, ballastSize)
		}
		if 33 > val {
			log.Fatalf("Expected a number greater than 33 for %s env variable but got %s", ballastEnvVarName, ballastSize)
		}
	} else {
		ballastSize = strconv.Itoa(memTotalSizeMiB * defaultMemoryBallastPercentage / 100)
		os.Setenv(ballastEnvVarName, ballastSize)
	}

	args := os.Args[1:]
	if !contains(args, "--mem-ballast-size-mib") {
		// Inject the command line flag that controls the ballast size.
		os.Args = append(os.Args, "--mem-ballast-size-mib="+ballastSize)
	}
	log.Printf("Set ballast to %s MiB", ballastSize)
}

// Validate and set the memory limit
func setMemoryLimit(memTotalSizeMiB int) {
	memLimit := 0
	// Check if the memory limit is specified via the env var
	memoryLimit := os.Getenv(memLimitMiBEnvVarName)
	// If not, calculate it from memTotalSizeMiB
	if memoryLimit == "" {
		memLimit = memTotalSizeMiB * defaultMemoryLimitPercentage / 100
		// The memory limit should be set to defaultMemoryLimitPercentage of total memory
		// while reserving a maximum of defaultMemoryLimitMaxMiB of memory.
		if (memTotalSizeMiB - memLimit) > defaultMemoryLimitMaxMiB {
			memLimit = defaultMemoryLimitMaxMiB
		}
	} else {
		memLimit, _ = strconv.Atoi(memoryLimit)
	}

	// Validate memoryLimit is sane
	args := os.Args[1:]
	b := getKeyValue(args, "--mem-ballast-size-mib")
	ballastSize, _ := strconv.Atoi(b)
	if (ballastSize * 2) > memLimit {
		log.Fatalf("Memory limit (%v) is less than 2x ballast (%v). Increase memory limit or decrease ballast size.", memLimit, ballastSize)
	}

	os.Setenv(memLimitMiBEnvVarName, strconv.Itoa(memLimit))
	log.Printf("Set memory limit to %d MiB", memLimit)
}

func runInteractive(params service.AppSettings) error {
	app, err := service.New(params)
	if err != nil {
		return fmt.Errorf("failed to construct the application: %w", err)
	}

	err = app.Run()
	if err != nil {
		return fmt.Errorf("application run finished with error: %w", err)
	}

	return nil
}
