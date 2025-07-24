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
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"

	agentlibstesting "github.com/elastic/elastic-agent-libs/testing"
)

var _ agentlibstesting.Driver = (*PlainTextDriver)(nil)

func TestPlainTextDriver(t *testing.T) {
	var buf bytes.Buffer
	driver := NewPlainTextDriver(&buf)

	driver.Run("test suite", func(d agentlibstesting.Driver) {
		d.Info("field1", "value1")
		d.Warn("field2", "reason2")
		d.Error("field3", errors.New("error3"))
		d.Fatal("field4", errors.New("error4"))
		d.Error("field5", nil)
		d.Run("sub-test", func(d agentlibstesting.Driver) {
			d.Info("sub-field1", "sub-value1")
			d.Result("some result data")
		})
	})

	expected := `test suite...
  field1: value1
  field2... WARN reason2
  field3... ERROR error3
  field4... ERROR error4
  field5... OK
  sub-test...
    sub-field1: sub-value1
    result:
      some result data
`

	// The output contains extra newlines between some of the calls, so we'll
	// compare line-by-line, ignoring empty lines.
	gotLinesRaw := strings.Split(buf.String(), "\n")
	var gotLines []string
	for _, l := range gotLinesRaw {
		if ll := strings.TrimRightFunc(l, unicode.IsSpace); ll != "" {
			gotLines = append(gotLines, ll)
		}
	}

	expectedLinesRaw := strings.Split(expected, "\n")
	var expectedLines []string
	for _, l := range expectedLinesRaw {
		if ll := strings.TrimRightFunc(l, unicode.IsSpace); ll != "" {
			expectedLines = append(expectedLines, ll)
		}
	}

	assert.Equal(t, expectedLines, gotLines,
		"The output from the PlainTextDriver does not match the expected output")
}
