// Copyright 2015-2017 trivago GmbH
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package producer

import (
	"compress/gzip"
	"errors"
	"fmt"
	"github.com/sirupsen/logrus"
	"github.com/trivago/gollum/core"
	"github.com/trivago/gollum/core/configs"
	"github.com/trivago/tgo/tio"
	"github.com/trivago/tgo/tmath"
	"github.com/trivago/tgo/tstrings"
	"github.com/trivago/tgo/tsync"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// File producer plugin
//
// The file producer writes messages to a file. This producer also allows log
// rotation and compression of the rotated logs. Folders in the file path will
// be created if necessary.
//
// Configuration example
//
//  myProducer:
//    Type: producer.File
//    File: "/var/log/gollum.log"
//    FileOverwrite: false
//    Permissions: "0664"
//    FolderPermissions: "0755"
//    Batch:
// 		MaxCount: 8192
//    	FlushCount: 4096
//    	TimeoutSec: 5
//    FlushTimeoutSec: 5
//    Rotation:
//		Enable: false
// 		Timestamp: 2006-01-02_15
//    	TimeoutMin: 1440
//    	SizeMB: 1024
// 		Compress: false
// 		ZeroPadding: 0
// 		At: 13:05
// 	  Prune:
//    	Count: 0
//    	AfterHours: 0
//    	TotalSizeMB: 0
//
// File contains the path to the log file to write. The wildcard character "*"
// can be used as a placeholder for the stream name.
// By default this is set to /var/log/gollum.log.
//
// FileOverwrite enables files to be overwritten instead of appending new data
// to it. This is set to false by default.
//
// Permissions accepts an octal number string that contains the unix file
// permissions used when creating a file. By default this is set to "0664".
//
// FolderPermissions accepts an octal number string that contains the unix file
// permissions used when creating a folder. By default this is set to "0755".
//
// Batch/MaxCount defines the maximum number of messages that can be buffered
// before a flush is mandatory. If the buffer is full and a flush is still
// underway or cannot be triggered out of other reasons, the producer will
// block. By default this is set to 8192.
//
// Batch/FlushCount defines the number of messages to be buffered before they are
// written to disk. This setting is clamped to BatchMaxCount.
// By default this is set to BatchMaxCount / 2.
//
// Batch/TimeoutSec defines the maximum number of seconds to wait after the last
// message arrived before a batch is flushed automatically. By default this is
// set to 5.
//
// FlushTimeoutSec sets the maximum number of seconds to wait before a flush is
// aborted during shutdown. By default this is set to 0, which does not abort
// the flushing procedure.
//
// Prune/Count removes old logfiles upon rotate so that only the given
// number of logfiles remain. Logfiles are located by the name defined by "File"
// and are pruned by date (followed by name).
// By default this is set to 0 which disables pruning.
//
// Prune/AfterHours removes old logfiles that are older than a given number
// of hours. By default this is set to 0 which disables pruning.
//
// Prune/TotalSizeMB removes old logfiles upon rotate so that only the
// given number of MBs are used by logfiles. Logfiles are located by the name
// defined by "File" and are pruned by date (followed by name).
// By default this is set to 0 which disables pruning.
type File struct {
	core.DirectProducer `gollumdoc:"embed_type"`

	// Public properties
	//
	// Rotate is public to make Pruner.Configure() callable (bug in treflect package)
	// Prune is public to make FileRotateConfig.Configure() callable (bug in treflect package)
	Rotate configs.RotateConfig `gollumdoc:"embed_type"`
	Pruner filePruner           `gollumdoc:"embed_type"`

	// configuration
	batchTimeout      time.Duration `config:"Batch/TimeoutSec" default:"5" metric:"sec"`
	batchMaxCount     int           `config:"Batch/MaxCount" default:"8192"`
	batchFlushCount   int           `config:"Batch/FlushCount" default:"4096"`
	flushTimeout      time.Duration `config:"FlushTimeoutSec" default:"0" metric:"sec"`
	overwriteFile     bool          `config:"FileOverwrite"`
	filePermissions   os.FileMode   `config:"Permissions" default:"0644"`
	folderPermissions os.FileMode   `config:"FolderPermissions" default:"0755"`

	// properties
	filesByStream map[core.MessageStreamID]*BatchedWriterAssembly
	files         map[string]*BatchedWriterAssembly
	fileDir       string
	fileName      string
	fileExt       string
	wildcardPath  bool
}

func init() {
	core.TypeRegistry.Register(File{})
}

// Configure initializes this producer with values from a plugin config.
func (prod *File) Configure(conf core.PluginConfigReader) {
	prod.Pruner.logger = prod.Logger

	prod.SetRollCallback(prod.rotateLog)
	prod.SetStopCallback(prod.close)

	prod.filesByStream = make(map[core.MessageStreamID]*BatchedWriterAssembly)
	prod.files = make(map[string]*BatchedWriterAssembly)
	prod.batchFlushCount = tmath.MinI(prod.batchFlushCount, prod.batchMaxCount)

	logFile := conf.GetString("File", "/var/log/gollum.log")
	prod.wildcardPath = strings.IndexByte(logFile, '*') != -1

	prod.fileDir = filepath.Dir(logFile)
	prod.fileExt = filepath.Ext(logFile)
	prod.fileName = filepath.Base(logFile)
	prod.fileName = prod.fileName[:len(prod.fileName)-len(prod.fileExt)]
}

// Produce writes to a buffer that is dumped to a file.
func (prod *File) Produce(workers *sync.WaitGroup) {
	prod.AddMainWorker(workers)
	prod.TickerMessageControlLoop(prod.writeMessage, prod.batchTimeout, prod.writeBatchOnTimeOut)
}

func (prod *File) getBatchedFile(streamID core.MessageStreamID, forceRotate bool) (*BatchedWriterAssembly, error) {
	var err error

	// get batchedFile from filesByStream[streamID] map
	if batchedFile, fileExists := prod.filesByStream[streamID]; fileExists {
		if rotate, err := prod.needsRotate(batchedFile, forceRotate); !rotate {
			return batchedFile, err // ### return, already open or error ###
		}
	}

	// todo: naming
	streamFile := prod.newStreamFile(streamID)

	// get batchedFile from files[path] and assure the file is correctly mapped
	batchedFile, fileExists := prod.files[streamFile.GetOriginalPath()]
	if !fileExists {
		// batchedFile does not yet exist: create and map it
		batchedFile = NewBatchedWriterAssembly(
			prod.batchMaxCount,
			prod.batchTimeout,
			prod.batchFlushCount,
			prod,
			prod.TryFallback,
			prod.flushTimeout,
			prod.Logger,
		)

		prod.files[streamFile.GetOriginalPath()] = batchedFile
		prod.filesByStream[streamID] = batchedFile
	} else if _, mappingExists := prod.filesByStream[streamID]; !mappingExists {
		// batchedFile exists but is not mapped: map it and see if we need to Rotate
		prod.filesByStream[streamID] = batchedFile
		if rotate, err := prod.needsRotate(batchedFile, forceRotate); !rotate {
			return batchedFile, err // ### return, already open or error ###
		}
	}

	// Assure directory is existing
	if _, err = streamFile.GetDir(); err != nil {
		return nil, err // ### return, missing directory ###
	}

	//todo: naming
	finalPath := streamFile.GetFinalPath(prod.Rotate)

	// Close existing batchedFile.writer
	if batchedFile.writer != nil {
		currentLog := batchedFile.writer
		batchedFile.writer = nil

		prod.Logger.Info("Rotated ", currentLog.Name(), " -> ", finalPath)
		go currentLog.Close() // close in subroutine for eventually compression in the background
	}

	// Update BatchedWriterAssembly writer and creation time
	batchedFile.writer, err = prod.newFileStateWriterDisk(finalPath)
	if err != nil {
		return batchedFile, err // ### return error ###
	}

	batchedFile.created = time.Now()

	// Create "current" symlink
	if prod.Rotate.Enabled {
		prod.createCurrentSymlink(finalPath, streamFile.GetSymlinkPath())
	}

	// Prune old logs if requested
	go prod.Pruner.Prune(streamFile.GetOriginalPath())

	return batchedFile, err
}

// NeedsRotate evaluate if the BatchedWriterAssembly need to rotate by the FileRotateConfig
func (prod *File) needsRotate(batchFile *BatchedWriterAssembly, forceRotate bool) (bool, error) {
	// File does not exist?
	if batchFile.writer == nil {
		return true, nil
	}

	// File can be accessed?
	if batchFile.writer.IsAccessible() == false {
		return false, errors.New("Can' access file to rotate")
	}

	// File needs rotation?
	if !prod.Rotate.Enabled {
		return false, nil
	}

	if forceRotate {
		return true, nil
	}

	// File is too large?
	if batchFile.writer.Size() >= prod.Rotate.SizeByte {
		return true, nil // ### return, too large ###
	}

	// File is too old?
	if time.Since(batchFile.created) >= prod.Rotate.Timeout {
		return true, nil // ### return, too old ###
	}

	// RotateAt crossed?
	if prod.Rotate.AtHour > -1 && prod.Rotate.AtMinute > -1 {
		now := time.Now()
		rotateAt := time.Date(now.Year(), now.Month(), now.Day(), prod.Rotate.AtHour, prod.Rotate.AtMinute, 0, 0, now.Location())

		if batchFile.created.Sub(rotateAt).Minutes() < 0 {
			return true, nil // ### return, too old ###
		}
	}

	// nope, everything is ok
	return false, nil
}

func (prod *File) createCurrentSymlink(source, target string) {
	symLinkNameTemporary := fmt.Sprintf("%s.tmp", target)

	os.Symlink(source, symLinkNameTemporary)
	os.Rename(symLinkNameTemporary, target)
}

func (prod *File) newFileStateWriterDisk(path string) (*batchedFileWriter, error) {
	openFlags := os.O_RDWR | os.O_CREATE | os.O_APPEND
	if prod.overwriteFile {
		openFlags |= os.O_TRUNC
	} else {
		openFlags |= os.O_APPEND
	}

	file, err := os.OpenFile(path, openFlags, prod.filePermissions)
	if err != nil {
		return nil, err // ### return error ###
	}

	obj := batchedFileWriter{
		file,
		prod.Rotate.Compress,
		nil,
		prod.Logger,
	}

	return &obj, nil
}

func (prod *File) newStreamFile(streamID core.MessageStreamID) streamFile {
	var fileDir, fileName, fileExt string

	if prod.wildcardPath {
		// Get state from filename (without timestamp, etc.)
		var streamName string
		switch streamID {
		case core.WildcardStreamID:
			streamName = "ALL"
		default:
			streamName = core.StreamRegistry.GetStreamName(streamID)
		}

		fileDir = strings.Replace(prod.fileDir, "*", streamName, -1)
		fileName = strings.Replace(prod.fileName, "*", streamName, -1)
		fileExt = strings.Replace(prod.fileExt, "*", streamName, -1)
	} else {
		// Simple case: only one file used
		fileDir = prod.fileDir
		fileName = prod.fileName
		fileExt = prod.fileExt
	}

	return streamFile{
		fileDir,
		fileName,
		fileExt,
		fmt.Sprintf("%s/%s%s", fileDir, fileName, fileExt),
		prod.folderPermissions,
	}
}

func (prod *File) rotateLog() {
	for streamID := range prod.filesByStream {
		if _, err := prod.getBatchedFile(streamID, true); err != nil {
			prod.Logger.Error("Rotate error: ", err)
		}
	}
}

func (prod *File) writeBatchOnTimeOut() {
	for _, batchedFile := range prod.files {
		batchedFile.FlushOnTimeOut()
	}
}

func (prod *File) writeMessage(msg *core.Message) {
	batchedFile, err := prod.getBatchedFile(msg.GetOrigStreamID(), false)
	if err != nil {
		prod.Logger.Error("Write error: ", err)
		prod.TryFallback(msg)
		return // ### return, fallback ###
	}

	batchedFile.batch.AppendOrFlush(msg, batchedFile.Flush, prod.IsActiveOrStopping, prod.TryFallback)
}

func (prod *File) close() {
	defer prod.WorkerDone()

	for _, batchedFile := range prod.files {
		batchedFile.Close()
	}
}

// -- streamFile --

type streamFile struct {
	dir               string
	name              string
	ext               string
	originalPath      string
	folderPermissions os.FileMode
}

// GetOriginalPath returns the base path from instantiation
func (streamFile *streamFile) GetOriginalPath() string {
	return streamFile.originalPath
}

// GetFinalPath returns the final file path with possible rotations
func (streamFile *streamFile) GetFinalPath(rotate configs.RotateConfig) string {
	return fmt.Sprintf("%s/%s", streamFile.dir, streamFile.GetFinalName(rotate))
}

// GetFinalName returns the final file name with possible rotations
func (streamFile *streamFile) GetFinalName(rotate configs.RotateConfig) string {
	var logFileName string

	// Generate the log filename based on rotation, existing files, etc.
	if !rotate.Enabled {
		logFileName = fmt.Sprintf("%s%s", streamFile.name, streamFile.ext)
	} else {
		timestamp := time.Now().Format(rotate.Timestamp)
		signature := fmt.Sprintf("%s_%s", streamFile.name, timestamp)
		maxSuffix := uint64(0)

		files, _ := ioutil.ReadDir(streamFile.dir)
		for _, file := range files {
			if strings.HasPrefix(file.Name(), signature) {
				// Special case.
				// If there is no extension, counter stays at 0
				// If there is an extension (and no count), parsing the "." will yield a counter of 0
				// If there is a count, parsing it will work as intended
				counter := uint64(0)
				if len(file.Name()) > len(signature) {
					counter, _ = tstrings.Btoi([]byte(file.Name()[len(signature)+1:]))
				}

				if maxSuffix <= counter {
					maxSuffix = counter + 1
				}
			}
		}

		if maxSuffix == 0 {
			logFileName = fmt.Sprintf("%s%s", signature, streamFile.ext)
		} else {
			formatString := "%s_%d%s"
			if rotate.ZeroPad > 0 {
				formatString = fmt.Sprintf("%%s_%%0%dd%%s", rotate.ZeroPad)
			}
			logFileName = fmt.Sprintf(formatString, signature, int(maxSuffix), streamFile.ext)
		}
	}

	return logFileName
}

// GetDir create file directory if it not exists and returns the dir name
func (streamFile *streamFile) GetDir() (string, error) {
	// Assure path is existing
	if err := os.MkdirAll(streamFile.dir, streamFile.folderPermissions); err != nil {
		return "", fmt.Errorf("Failed to create %s because of %s", streamFile.dir, err.Error())
	}

	return streamFile.dir, nil
}

// GetSymlinkPath returns a symlink path for the current file
func (streamFile *streamFile) GetSymlinkPath() string {
	return fmt.Sprintf("%s/%s_current%s", streamFile.dir, streamFile.name, streamFile.ext)
}

// -- batchedFileWriter --

type batchedFileWriter struct {
	file            *os.File
	compressOnClose bool
	stats           os.FileInfo
	logger          logrus.FieldLogger
}

// Write is part of the BatchedWriter interface and wraps the file.Write() implementation
func (w *batchedFileWriter) Write(p []byte) (n int, err error) {
	return w.file.Write(p)
}

// Name is part of the BatchedWriter interface and wraps the file.Name() implementation
func (w *batchedFileWriter) Name() string {
	return w.file.Name()
}

// Size is part of the BatchedWriter interface and wraps the file.Stat().Size() implementation
func (w *batchedFileWriter) Size() int64 {
	stats, err := w.getStats()
	if err != nil {
		return 0
	}
	return stats.Size()
}

//func (w *batchedFileWriter) Created() time.Time {
//	return w.file.
//}

// Size is part of the BatchedWriter interface and check if the writer can access his file
func (w *batchedFileWriter) IsAccessible() bool {
	_, err := w.getStats()
	if err != nil {
		return false
	}

	return true
}

// Size is part of the Close interface and handle the file close or compression call
func (w *batchedFileWriter) Close() error {
	if w.compressOnClose {
		return w.compressAndCloseLog()
	}

	return w.file.Close()
}

func (w *batchedFileWriter) getStats() (os.FileInfo, error) {
	if w.stats != nil {
		return w.stats, nil
	}

	stats, err := w.file.Stat()
	if err != nil {
		return nil, err
	}

	w.stats = stats
	return w.stats, nil
}

func (w *batchedFileWriter) compressAndCloseLog() error {
	// Generate file to zip into
	sourceFileName := w.Name()
	sourceDir, sourceBase, _ := tio.SplitPath(sourceFileName)

	targetFileName := fmt.Sprintf("%s/%s.gz", sourceDir, sourceBase)

	targetFile, err := os.OpenFile(targetFileName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		w.logger.Error("Compress error:", err)
		w.file.Close()
		return err
	}

	// Create zipfile and compress data
	w.logger.Info("Compressing " + sourceFileName)

	w.file.Seek(0, 0)
	targetWriter := gzip.NewWriter(targetFile)
	spin := tsync.NewSpinner(tsync.SpinPriorityHigh)

	for err == nil {
		_, err = io.CopyN(targetWriter, w.file, 1<<20) // 1 MB chunks
		spin.Yield()                                   // Be async!
	}

	// Cleanup
	w.file.Close()
	targetWriter.Close()
	targetFile.Close()

	if err != nil && err != io.EOF {
		w.logger.Warning("Compression failed:", err)
		err = os.Remove(targetFileName)
		if err != nil {
			w.logger.Error("Compressed file remove failed:", err)
		}
		return err
	}

	// Remove original log
	err = os.Remove(sourceFileName)
	if err != nil {
		w.logger.Error("Uncompressed file remove failed:", err)
		return err
	}

	return nil
}

// -- filePruner --

type filePruner struct {
	pruneCount int   `config:"Prune/Count" default:"0"`
	pruneHours int   `config:"Prune/AfterHours" default:"0"`
	pruneSize  int64 `config:"Prune/TotalSizeMB" default:"0" metric:"mb"`
	rotate     configs.RotateConfig
	logger     logrus.FieldLogger
}

// Configure initializes this object with values from a plugin config.
func (pruner *filePruner) Configure(conf core.PluginConfigReader) {
	if pruner.pruneSize > 0 && pruner.rotate.SizeByte > 0 {
		pruner.pruneSize -= pruner.rotate.SizeByte >> 20
		if pruner.pruneSize <= 0 {
			pruner.pruneCount = 1
			pruner.pruneSize = 0
		}
	}
}

// Prune starts prune methods by hours, by count and by size
func (pruner *filePruner) Prune(baseFilePath string) {
	if pruner.pruneHours > 0 {
		pruner.pruneByHour(baseFilePath, pruner.pruneHours)
	}
	if pruner.pruneCount > 0 {
		pruner.pruneByCount(baseFilePath, pruner.pruneCount)
	}
	if pruner.pruneSize > 0 {
		pruner.pruneToSize(baseFilePath, pruner.pruneSize)
	}
}

func (pruner *filePruner) pruneByHour(baseFilePath string, hours int) {
	baseDir, baseName, _ := tio.SplitPath(baseFilePath)

	files, err := tio.ListFilesByDateMatching(baseDir, baseName+".*")
	if err != nil {
		pruner.logger.Error("Error pruning files: ", err)
		return // ### return, error ###
	}

	pruneDate := time.Now().Add(time.Duration(-hours) * time.Hour)

	for i := 0; i < len(files) && files[i].ModTime().Before(pruneDate); i++ {
		filePath := fmt.Sprintf("%s/%s", baseDir, files[i].Name())
		if err := os.Remove(filePath); err != nil {
			pruner.logger.Errorf("Failed to prune \"%s\": %s", filePath, err.Error())
		} else {
			pruner.logger.Infof("Pruned \"%s\"", filePath)
		}
	}
}

func (pruner *filePruner) pruneByCount(baseFilePath string, count int) {
	baseDir, baseName, _ := tio.SplitPath(baseFilePath)

	files, err := tio.ListFilesByDateMatching(baseDir, baseName+".*")
	if err != nil {
		pruner.logger.Error("Error pruning files: ", err)
		return // ### return, error ###
	}

	numFilesToPrune := len(files) - count
	if numFilesToPrune < 1 {
		return // ## return, nothing to prune ###
	}

	for i := 0; i < numFilesToPrune; i++ {
		filePath := fmt.Sprintf("%s/%s", baseDir, files[i].Name())
		if err := os.Remove(filePath); err != nil {
			pruner.logger.Errorf("Failed to prune \"%s\": %s", filePath, err.Error())
		} else {
			pruner.logger.Infof("Pruned \"%s\"", filePath)
		}
	}
}

func (pruner *filePruner) pruneToSize(baseFilePath string, maxSize int64) {
	baseDir, baseName, _ := tio.SplitPath(baseFilePath)

	files, err := tio.ListFilesByDateMatching(baseDir, baseName+".*")
	if err != nil {
		pruner.logger.Error("Error pruning files: ", err)
		return // ### return, error ###
	}

	totalSize := int64(0)
	for _, file := range files {
		totalSize += file.Size()
	}

	for _, file := range files {
		if totalSize <= maxSize {
			return // ### return, done ###
		}
		filePath := fmt.Sprintf("%s/%s", baseDir, file.Name())
		if err := os.Remove(filePath); err != nil {
			pruner.logger.Errorf("Failed to prune \"%s\": %s", filePath, err.Error())
		} else {
			pruner.logger.Infof("Pruned \"%s\"", filePath)
			totalSize -= file.Size()
		}
	}
}
