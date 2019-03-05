package goDB

import (
	"errors"
	"os"
	"strconv"

	"github.com/els0r/goProbe/pkg/goDB/lz4"
)

const (
	// BufSize allocates space for the header (512 slots for 64bit integers)
	BufSize = 4096
	// NumElements is the number of available header slots
	NumElements = BufSize / 8 // 512
)

// GPFile implements the binary data file used to store goProbe's flows
type GPFile struct {
	// The file header //
	// Contains 512 64 bit addresses pointing to the end
	// (+1 byte) of each compressed block and the lookup
	// table which stores 512 timestamps as int64 for
	// lookup without having to parse the file
	blocks     []int64
	timestamps []int64
	lengths    []int64

	// The path to the file
	filename string
	curFile  *os.File
	wBuf     []byte

	lastSeekPos int64

	// governs how data blocks are (de-)compressed
	encoder Encoder
}

// GPFileOption defines optional arguments to GPFile
type GPFileOption func(*GPFile)

// WithGPFileEncoding allows to set the compression and decompression implementation.
func WithGPFileEncoding(e Encoder) GPFileOption {
	return func(gpf *GPFile) {
		gpf.encoder = e
		return
	}
}

// NewGPFile returns a new GPFile object to read and write goProbe flow data
func NewGPFile(p string, opts ...GPFileOption) (*GPFile, error) {
	var (
		bufH          = make([]byte, BufSize)
		bufTS         = make([]byte, BufSize)
		bufLen        = make([]byte, BufSize)
		f             *os.File
		nH, nTS, nLen int
		err           error
	)

	// open file if it exists and read header, otherwise create it
	// and write empty header
	if _, err = os.Stat(p); err == nil {
		if f, err = os.Open(p); err != nil {
			return nil, err
		}
		if nH, err = f.Read(bufH); err != nil {
			return nil, err
		}
		if nTS, err = f.Read(bufTS); err != nil {
			return nil, err
		}
		if nLen, err = f.Read(bufLen); err != nil {
			return nil, err
		}
	} else {
		if f, err = os.Create(p); err != nil {
			return nil, err
		}
		if nH, err = f.Write(bufH); err != nil {
			return nil, err
		}
		if nTS, err = f.Write(bufTS); err != nil {
			return nil, err
		}
		if nLen, err = f.Write(bufLen); err != nil {
			return nil, err
		}
		f.Sync()
	}

	if nH != BufSize {
		return nil, errors.New("Invalid header (blocks)")
	}
	if nTS != BufSize {
		return nil, errors.New("Invalid header (lookup table)")
	}
	if nLen != BufSize {
		return nil, errors.New("Invalid header (block lengths)")
	}

	// read the header information
	var h = make([]int64, NumElements)
	var ts = make([]int64, NumElements)
	var le = make([]int64, NumElements)
	var pos int
	for i := 0; i < NumElements; i++ {
		h[i] = int64(bufH[pos])<<56 | int64(bufH[pos+1])<<48 | int64(bufH[pos+2])<<40 | int64(bufH[pos+3])<<32 | int64(bufH[pos+4])<<24 | int64(bufH[pos+5])<<16 | int64(bufH[pos+6])<<8 | int64(bufH[pos+7])
		ts[i] = int64(bufTS[pos])<<56 | int64(bufTS[pos+1])<<48 | int64(bufTS[pos+2])<<40 | int64(bufTS[pos+3])<<32 | int64(bufTS[pos+4])<<24 | int64(bufTS[pos+5])<<16 | int64(bufTS[pos+6])<<8 | int64(bufTS[pos+7])
		le[i] = int64(bufLen[pos])<<56 | int64(bufLen[pos+1])<<48 | int64(bufLen[pos+2])<<40 | int64(bufLen[pos+3])<<32 | int64(bufLen[pos+4])<<24 | int64(bufLen[pos+5])<<16 | int64(bufLen[pos+6])<<8 | int64(bufLen[pos+7])
		pos += 8
	}

	// the GP File uses LZ4 data block compression by default
	gpf := &GPFile{h, ts, le, p, f, make([]byte, BufSize*3), 0, lz4.New()}

	// apply functional options
	for _, opt := range opts {
		opt(gpf)
	}

	return gpf, nil
}

// BlocksUsed returns how many slots are already taken in the GP file
func (f *GPFile) BlocksUsed() (int, error) {
	for i := 0; i < NumElements; i++ {
		if f.timestamps[i] == 0 && f.blocks[i] == 0 && f.lengths[i] == 0 {
			return i, nil
		}
	}
	return -1, errors.New("Could not retrieve number of allocated blocks")
}

// ReadBlock returns the data for a given block in the file
func (f *GPFile) ReadBlock(block int) ([]byte, error) {
	if f.timestamps[block] == 0 && f.blocks[block] == 0 && f.lengths[block] == 0 {
		return nil, errors.New("Block " + strconv.Itoa(block) + " is empty")
	}

	var (
		err     error
		seekPos int64 = BufSize * 3
		readLen int64
	)

	// Check if file has already been opened for reading. If not, open it
	if f.curFile == nil {
		if f.curFile, err = os.OpenFile(f.filename, os.O_RDONLY, 0600); err != nil {
			return nil, err
		}
	}

	// If first block is requested, set seek position to end of header and read length of
	// first block. Otherwise start at last block's end
	readLen = f.blocks[block] - BufSize*3
	if block != 0 {
		seekPos = f.blocks[block-1]
		readLen = f.blocks[block] - f.blocks[block-1]
	}

	// if the file is read continuously, do not seek
	if seekPos != f.lastSeekPos {
		if _, err = f.curFile.Seek(seekPos, 0); err != nil {
			return nil, err
		}

		f.lastSeekPos = seekPos
	}

	// prepare data slices for decompression
	var (
		uncompLen int
		bufComp   = make([]byte, readLen)
		buf       = make([]byte, f.lengths[block])
	)

	uncompLen, err = f.encoder.Decompress(bufComp, buf, f.curFile)
	if int64(uncompLen) != readLen {
		return nil, errors.New("Incorrect number of bytes read for decompression")
	}

	return buf, nil
}

// ReadTimedBlock searches if a block for a given timestamp exists and returns in its data
func (f *GPFile) ReadTimedBlock(timestamp int64) ([]byte, error) {
	for i := 0; i < NumElements; i++ {
		if f.timestamps[i] == timestamp {
			return f.ReadBlock(i)
		}
	}

	return nil, errors.New("Timestamp " + strconv.Itoa(int(timestamp)) + " not found")
}

// WriteTimedBlock writes data to the file for a given timestamp
func (f *GPFile) WriteTimedBlock(timestamp int64, data []byte) error {
	var (
		nextFreeBlock = int64(-1)
		curWfile      *os.File
		err           error
		nWrite        int
		newPos        int64
	)

	for newPos = 0; newPos < NumElements; newPos++ {
		curTstamp := f.timestamps[newPos]
		if curTstamp == timestamp {
			return errors.New("Timestamp" + strconv.Itoa(int(curTstamp)) + " already exists in file " + f.filename)
		} else if curTstamp == 0 {
			if newPos != 0 {
				nextFreeBlock = f.blocks[newPos-1]
			} else {
				nextFreeBlock = BufSize * 3
			}
			break
		}
	}

	if nextFreeBlock == -1 {
		return errors.New("File is full")
	}

	if curWfile, err = os.OpenFile(f.filename, os.O_APPEND|os.O_WRONLY, 0600); err != nil {
		return err
	}

	// compress the data
	nWrite, err = f.encoder.Compress(data, curWfile)
	if err != nil {
		return err
	}
	curWfile.Close()

	// Update header
	f.blocks[newPos] = nextFreeBlock + int64(nWrite)
	f.timestamps[newPos] = timestamp
	f.lengths[newPos] = int64(len(data))

	var pos int
	for i := 0; i < NumElements; i++ {
		for j := 0; j < 8; j++ {
			f.wBuf[pos+j] = byte(f.blocks[i] >> uint(56-(j*8)))
			f.wBuf[BufSize+pos+j] = byte(f.timestamps[i] >> uint(56-(j*8)))
			f.wBuf[BufSize+BufSize+pos+j] = byte(f.lengths[i] >> uint(56-(j*8)))
		}
		pos += 8
	}

	if curWfile, err = os.OpenFile(f.filename, os.O_WRONLY, 0600); err != nil {
		return err
	}
	if _, err = curWfile.Write(f.wBuf); err != nil {
		return err
	}
	curWfile.Close()

	return nil
}

// GetBlocks returns the in-file location for all data blocks
func (f *GPFile) GetBlocks() []int64 {
	return f.blocks
}

// GetTimestamps returns all timestamps under which data blocks were stored
func (f *GPFile) GetTimestamps() []int64 {
	return f.timestamps
}

// Close closes the underlying file
func (f *GPFile) Close() error {
	if f.curFile != nil {
		return f.curFile.Close()
	}
	return nil
}
