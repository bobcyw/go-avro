package avro

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"math"
)

// Support decoding the avro Object Container File format.
// Spec: http://avro.apache.org/docs/1.7.7/spec.html#Object+Container+Files

const objHeaderSchemaRaw = `{"type": "record", "name": "org.apache.avro.file.Header",
 "fields" : [
   {"name": "magic", "type": {"type": "fixed", "name": "Magic", "size": 4}},
   {"name": "meta", "type": {"type": "map", "values": "bytes"}},
   {"name": "sync", "type": {"type": "fixed", "name": "Sync", "size": 16}}
  ]
}`

var objHeaderSchema = MustParseSchema(objHeaderSchemaRaw)

const (
	version    byte = 1
	sync_size       = 16
	schema_key      = "avro.schema"
	codec_key       = "avro.codec"
)

var magic []byte = []byte{'O', 'b', 'j', version}

var syncBuffer = make([]byte, sync_size)

// DataFileReader is a reader for Avro Object Container Files.
// More here: https://avro.apache.org/docs/current/spec.html#Object+Container+Files
type DataFileReader struct {
	data         []byte
	header       *objFileHeader
	block        *DataBlock
	dec          Decoder
	blockDecoder Decoder
	datum        DatumReader
}

// The header for object container files
type objFileHeader struct {
	Magic []byte            `avro:"magic"`
	Meta  map[string][]byte `avro:"meta"`
	Sync  []byte            `avro:"sync"`
}

func readObjFileHeader(dec *BinaryDecoder) (*objFileHeader, error) {
	reader := NewSpecificDatumReader()
	reader.SetSchema(objHeaderSchema)
	header := &objFileHeader{}
	err := reader.Read(header, dec)
	return header, err
}

// Creates a new DataFileReader for a given file and using the given DatumReader to read the data from that file.
// May return an error if the file contains invalid data or is just missing.
func NewDataFileReader(filename string, datumReader DatumReader) (*DataFileReader, error) {
	if buf, err := ioutil.ReadFile(filename); err != nil {
		return nil, err
	} else {
		if len(buf) < len(magic) || !bytes.Equal(magic, buf[0:4]) {
			return nil, NotAvroFile
		}

		dec := NewBinaryDecoder(buf)
		blockDecoder := NewBinaryDecoder(nil)
		reader := &DataFileReader{
			data:         buf,
			dec:          dec,
			blockDecoder: blockDecoder,
			datum:        datumReader,
		}

		if reader.header, err = readObjFileHeader(dec); err != nil {
			return nil, err
		}

		schema, err := ParseSchema(string(reader.header.Meta[schema_key]))
		if err != nil {
			return nil, err
		}
		reader.datum.SetSchema(schema)
		reader.block = &DataBlock{}

		if reader.hasNextBlock() {
			if err := reader.NextBlock(); err != nil {
				return nil, err
			}
		}

		return reader, nil
	}
}

// Switches the reading position in this DataFileReader to a provided value.
func (this *DataFileReader) Seek(pos int64) {
	this.dec.Seek(pos)
}

func (this *DataFileReader) hasNext() (bool, error) {
	if this.block.BlockRemaining == 0 {
		if int64(this.block.BlockSize) != this.blockDecoder.Tell() {
			return false, BlockNotFinished
		}
		if this.hasNextBlock() {
			if err := this.NextBlock(); err != nil {
				return false, err
			}
		} else {
			return false, nil
		}
	}
	return true, nil
}

func (this *DataFileReader) hasNextBlock() bool {
	return int64(len(this.data)) > this.dec.Tell()
}

// Reads the next value from file and fills the given value with data.
// First return value indicates whether the read was successful.
// Second return value indicates whether there was an error while reading data.
// Returns (false, nil) when no more data left to read.
func (this *DataFileReader) Next(v interface{}) (bool, error) {
	if hasNext, err := this.hasNext(); err != nil {
		return false, err
	} else {
		if hasNext {
			err := this.datum.Read(v, this.blockDecoder)
			if err != nil {
				return false, err
			}
			this.block.BlockRemaining--
			return true, nil
		} else {
			return false, nil
		}
	}
}

// Tells this DataFileReader to skip current block and move to next one.
// May return an error if the block is malformed or no more blocks left to read.
func (this *DataFileReader) NextBlock() error {
	if blockCount, err := this.dec.ReadLong(); err != nil {
		return err
	} else {
		if blockSize, err := this.dec.ReadLong(); err != nil {
			return err
		} else {
			if blockSize > math.MaxInt32 || blockSize < 0 {
				return errors.New(fmt.Sprintf("Block size invalid or too large: %d", blockSize))
			}

			block := this.block
			if block.Data == nil || int64(len(block.Data)) < blockSize {
				block.Data = make([]byte, blockSize)
			}
			block.BlockRemaining = blockCount
			block.NumEntries = blockCount
			block.BlockSize = int(blockSize)
			this.dec.ReadFixedWithBounds(block.Data, 0, int(block.BlockSize))
			this.dec.ReadFixed(syncBuffer)
			if !bytes.Equal(syncBuffer, this.header.Sync) {
				return InvalidSync
			}
			this.blockDecoder.SetBlock(this.block)
		}
	}
	return nil
}
