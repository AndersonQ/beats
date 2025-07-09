//go:build integration

package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/elastic/beats/v7/libbeat/tests/integration"
)

func TestFilestreamGZIPLogRotation_2_rotated_file(t *testing.T) {
	want1stLines := make([]string, 0, 100)
	want2ndLines := make([]string, 0, 250)
	var dataPlain1stHalf []byte
	for i := range 100 {
		l := fmt.Sprintf("%d: 1st 1/2 file before rotation log line", i)
		want1stLines = append(want1stLines, l)
		dataPlain1stHalf = append(dataPlain1stHalf, []byte(l+"\n")...)
	}
	var dataPlain2ndHalf []byte
	for i := range 100 {
		l := fmt.Sprintf("%d: 2nd 1/2 file after rotation log line", i)
		want2ndLines = append(want2ndLines, l)
		dataPlain2ndHalf = append(dataPlain2ndHalf, []byte(l+"\n")...)
	}

	var dataPlainLatestRotation []byte
	for i := range 100 {
		l := fmt.Sprintf("%d: latest rotated file", i)
		want2ndLines = append(want2ndLines, l)
		dataPlainLatestRotation = append(dataPlainLatestRotation, []byte(l+"\n")...)
	}

	var dataPlainNewActive []byte
	for i := range 50 { // ensure it's smaller than the original
		l := fmt.Sprintf("%d: new plain file after truncation", i)
		want2ndLines = append(want2ndLines, l)
		dataPlainNewActive = append(dataPlainNewActive, []byte(l+"\n")...)
	}

	// dataGZOldestRotation := append(dataPlain1stHalf, dataPlain2ndHalf...)
	dataGZLatestRotation := dataPlainLatestRotation

	filebeat := integration.NewBeat(
		t,
		"filebeat",
		"../../filebeat.test",
	)
	tempDir := filebeat.TempDir()
	logFileBaseName := "plain.log"
	logPathActive := filepath.Join(tempDir, logFileBaseName)
	logPathLatestRotation := filepath.Join(tempDir, logFileBaseName+".1")
	logPathOldestRotation := filepath.Join(tempDir, logFileBaseName+".2")

	// 1st half of the file to simulate the rotation before filebeat finishes
	// reading the file
	err := os.WriteFile(logPathActive, dataPlain1stHalf, 0644)
	require.NoError(t, err, "could not write gzip file to disk")

	outputFilePattern := "output-file"
	cfg := fmt.Sprintf(`
filebeat.inputs:
  - type: filestream
    id: "test-filestream"
    paths:
      - %s
    rotation.external.strategy.copytruncate.suffix_regex: \.\d$
output.file:
  enabled: true
  path: %s
  filename: "%s"
logging.level: debug
`, logPathActive+"*", filebeat.TempDir(), outputFilePattern)

	filebeat.WriteConfigFile(cfg)
	filebeat.Start()

	eofLog := fmt.Sprintf("End of file reached: %s; Backoff now.", logPathActive)
	filebeat.WaitForLogs(
		eofLog,
		30*time.Second,
		"Filebeat did not reach EOF. Did not find log [%s]",
		eofLog,
	)
	// 1st file is ingested, stop filebeat and do the log rotation
	filebeat.Stop()

	// ============= simulate full log rotation, with the renaming =============

	// rotate the active file "with data not yet read"
	//   add the data to active log file
	active, err := os.OpenFile(logPathActive, os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err, "could not open active log file")
	_, err = active.Write(dataPlain2ndHalf)
	require.NoError(t, err, "could not append to active log file")
	require.NoError(t, active.Close(), "could not close active log file")

	//   rotate the file
	//     open active log file
	active, err = os.OpenFile(logPathActive, os.O_RDWR|os.O_APPEND, 0644)
	require.NoError(t, err, "could not open active log file")
	//     create rotated file
	rotated1, err := os.OpenFile(logPathLatestRotation, os.O_WRONLY|os.O_CREATE, 0644)
	require.NoError(t, err, "could not create rotated log file")
	//     copy data
	_, err = io.Copy(rotated1, active)
	require.NoError(t, err, "could not copy active file to rotated file")
	//     close rotated file
	require.NoError(t, rotated1.Close(), "could not close rotated file")
	//     truncate active file
	require.NoError(t, active.Truncate(0), "could not close rotated file")
	//     close active file
	require.NoError(t, active.Close(), "could not close active file")

	//     write new data to active log file
	active, err = os.OpenFile(logPathActive, os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err, "could not open active log file")
	_, err = active.Write(dataGZLatestRotation)
	require.NoError(t, err, "could not append to active log file")
	require.NoError(t, active.Close(), "could not close active log file")

	//   rotate again
	//     rename already rotated files
	err = os.Rename(logPathLatestRotation, logPathOldestRotation)
	require.NoError(t, err, "could rename latest rotation to oldest rotation")
	//     open active log file
	active, err = os.OpenFile(logPathActive, os.O_RDWR|os.O_APPEND, 0644)
	require.NoError(t, err, "could not open active log file")
	//     create latest rotated file
	rotated1, err = os.OpenFile(logPathLatestRotation, os.O_WRONLY|os.O_CREATE, 0644)
	require.NoError(t, err, "could not create rotated log file")
	//     copy data
	_, err = io.Copy(rotated1, active)
	require.NoError(t, err, "could not copy active file to rotated file")
	//     close rotated file
	require.NoError(t, rotated1.Close(), "could not close rotated file")
	//     truncate active file
	require.NoError(t, active.Truncate(0), "could not close rotated file")
	//     close active file
	require.NoError(t, active.Close(), "could not close active file")

	filebeat.Start()
	// Wait filebeat to finish the gzipped files
	eofLog = fmt.Sprintf("End of file reached: %s; Backoff now.", logPathLatestRotation)
	filebeat.WaitForLogs(
		eofLog,
		30*time.Second,
		"Filebeat did not reach EOF. Did not find log [%s]",
		eofLog,
	)

	eofLog = fmt.Sprintf("End of file reached: %s; Backoff now.", logPathOldestRotation)
	filebeat.WaitForLogs(
		eofLog,
		30*time.Second,
		"Filebeat did not reach EOF. Did not find log [%s]",
		eofLog,
	)

	// Wait filebeat to finish the original file with new content
	eofLog = fmt.Sprintf("End of file reached: %s; Backoff now.", logPathActive)
	filebeat.WaitForLogs(
		eofLog,
		30*time.Second,
		"Filebeat did not reach EOF. Did not find log [%s]",
		eofLog,
	)

	filebeat.Stop()

	// So far so good. Now check the output

	globPattern := outputFilePattern + "-*.ndjson"
	files, err := filepath.Glob(filepath.Join(tempDir, globPattern))
	require.NoError(t, err, "could not glob output file pattern")
	require.Lenf(t, files, 2,
		"expected only 2 output files. Glob pattern '%s'", globPattern)

	slices.SortFunc(files, func(a, b string) int {
		if len(a) < len(b) {
			return -1
		}
		if len(a) > len(b) {
			return 1
		}
		if len(a) == len(b) {
			return 0
		}

		panic("unreachable")
	})

	got, err := os.ReadFile(files[0])
	require.NoError(t, err, "could not open output file")
	// 1st file: check that all lines have been published
	matchPublishedLines(t, got, want1stLines)

	got, err = os.ReadFile(files[1])
	require.NoError(t, err, "could not open output file")
	matchPublishedLines(t, got, want2ndLines)
}

func matchPublishedLines(t *testing.T, got []byte, want []string) {
	gotLinesJSON := strings.Split(strings.TrimSpace(string(got)), "\n")
	assert.Equal(t, len(want), len(gotLinesJSON), "unexpected number of events")

	gotLines := make([]string, len(gotLinesJSON))

	logLine := struct {
		Message string `json:"message"`
	}{}
	for i, line := range gotLinesJSON {
		err := json.Unmarshal([]byte(line), &logLine)
		require.NoError(t, err, "could not Unmarshal log line")
		gotLines[i] = logLine.Message
	}

	slices.Sort(gotLines)
	slices.Sort(want)

	assert.Equal(t, want, gotLines, "not all lines match")
}

func assertLogFieldsEqual(t *testing.T, wantPath, gotPath string) {
	t.Helper()

	type event struct {
		Message string `json:"message"`
		Log     struct {
			Offset int64 `json:"offset"`
		} `json:"log"`
	}

	open := func(path string) *bufio.Scanner {
		f, err := os.Open(path)
		require.NoError(t, err, "opening file %s", path)
		t.Cleanup(func() { _ = f.Close() })
		return bufio.NewScanner(f)
	}

	wantScanner := open(wantPath)
	gotScanner := open(gotPath)

	line := 1
	for {
		wantOK := wantScanner.Scan()
		gotOK := gotScanner.Scan()

		if !wantOK || !gotOK {
			assert.Equal(t, wantOK, gotOK,
				"different number of lines: want EOF=%v, got EOF=%v at line %d",
				!wantOK, !gotOK, line,
			)
			return
		}

		var wantEv, gotEv event
		if err := json.Unmarshal(wantScanner.Bytes(), &wantEv); err != nil {
			t.Fatalf("failed to unmarshal want JSON at line %d: %v", line, err)
		}
		if err := json.Unmarshal(gotScanner.Bytes(), &gotEv); err != nil {
			t.Fatalf("failed to unmarshal got JSON at line %d: %v", line, err)
		}

		if wantEv.Message != gotEv.Message ||
			wantEv.Log.Offset != gotEv.Log.Offset {
			t.Errorf("line %d mismatch:\n\tmessage: want '%q got %q\n\toffset:  want %d got %d",
				line, wantEv.Message, gotEv.Message, wantEv.Log.Offset, gotEv.Log.Offset)
		}
		line++
	}
}
