// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package mcp

import (
	"fmt"
	"io"
	"strings"

	agentlibstesting "github.com/elastic/elastic-agent-libs/testing"
)

// PlainTextDriver outputs test result to the given stdout/stderr descriptors
// as plain text.
type PlainTextDriver struct {
	w        io.Writer
	level    int
	reported bool
	result   string
}

// NewPlainTextDriver initializes and returns a new plain text driver with output to given file
func NewPlainTextDriver(w io.Writer) *PlainTextDriver {
	return &PlainTextDriver{
		w:        w,
		level:    0,
		reported: true,
	}
}

// Run executes a sub-test.
func (d *PlainTextDriver) Run(name string, f func(agentlibstesting.Driver)) {
	if !d.reported {
		fmt.Fprintln(d.w, "")
	}
	d.printf("%s...", name)

	// Run sub func
	driver := &PlainTextDriver{
		w:     d.w,
		level: d.level + 1,
	}
	f(driver)

	if !driver.reported {
		fmt.Fprint(driver.w, " OK\n")
		driver.reported = true
	}

	if driver.result != "" {
		driver.Info("result", driver.indent(driver.result))
	}

	d.reported = true
}

// Info prints an information line.
func (d *PlainTextDriver) Info(field, value string) {
	if !d.reported {
		fmt.Fprintln(d.w, "")
	}
	d.printf("%s: %s\n", field, value)
	d.reported = true
}

// Warn prints a warning line.
func (d *PlainTextDriver) Warn(field, reason string) {
	if !d.reported {
		fmt.Fprintln(d.w, "")
	}
	d.printf("%s... ", field)
	fmt.Fprint(d.w, "WARN ")
	fmt.Fprintln(d.w, reason)
	d.reported = true
}

// Error prints an error line if err is not nil.
func (d *PlainTextDriver) Error(field string, err error) {
	if err == nil {
		d.ok(field)
		return
	}
	d.error(field, err)
}

// Fatal prints an error line if err is not nil.
func (d *PlainTextDriver) Fatal(field string, err error) {
	if err == nil {
		d.ok(field)
		return
	}
	d.error(field, err)
}

// Result sets the result data to be printed at the end of a sub-test.
func (d *PlainTextDriver) Result(data string) {
	d.result = data
}

func (d *PlainTextDriver) ok(field string) {
	if !d.reported {
		fmt.Fprintln(d.w, "")
	}
	d.printf("%s... ", field)
	fmt.Fprint(d.w, "OK\n")
	d.reported = true
}

func (d *PlainTextDriver) error(field string, err error) {
	if !d.reported {
		fmt.Fprintln(d.w, "")
	}
	d.printf("%s... ", field)
	fmt.Fprint(d.w, "ERROR ")
	fmt.Fprintln(d.w, err.Error())
	d.reported = true
}

func (d *PlainTextDriver) printf(format string, args ...interface{}) {
	for i := 0; i < d.level; i++ {
		fmt.Fprint(d.w, "  ")
	}
	fmt.Fprintf(d.w, format, args...)
}

func (d *PlainTextDriver) indent(data string) string {
	res := "\n"
	for _, line := range strings.Split(data, "\n") {
		res += strings.Repeat(" ", (d.level+1)*2) + line + "\n"
	}
	return res
}
