package slotagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const MaxFrameBytes = 64 << 10

var ErrFrameTooLarge = errors.New("slotagent: frame too large")

func ReadFrame(reader io.Reader) (Frame, error) {
	var line []byte
	buffer := make([]byte, 1)
	for len(line) <= MaxFrameBytes {
		n, err := reader.Read(buffer)
		if n == 1 {
			if buffer[0] == '\n' {
				break
			}
			if buffer[0] != '\r' {
				line = append(line, buffer[0])
			}
		}
		if err != nil {
			if err == io.EOF && len(line) == 0 {
				return Frame{}, io.EOF
			}
			return Frame{}, err
		}
	}
	if len(line) > MaxFrameBytes {
		return Frame{}, ErrFrameTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var frame Frame
	if err := decoder.Decode(&frame); err != nil {
		return Frame{}, ErrInvalidMessage
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Frame{}, ErrInvalidMessage
	}
	if err := frame.Validate(); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

func (f Frame) Validate() error {
	switch f.Kind {
	case "command":
		if f.Request == nil || f.Response != nil || f.Worker != nil {
			return ErrInvalidMessage
		}
		return f.Request.Validate()
	case "response":
		if f.Response == nil || f.Request != nil || f.Worker != nil {
			return ErrInvalidMessage
		}
		return f.Response.Validate()
	case "worker_request", "worker_event":
		if f.Worker == nil || f.Request != nil || f.Response != nil || !safeID(f.SlotID, 128) || !safeID(f.RequestID, 128) || !safeID(f.LeaseID, 160) || f.EnvironmentGeneration == 0 {
			return ErrInvalidMessage
		}
		return ValidateWorkerEnvelope(*f.Worker)
	default:
		return ErrUnsupported
	}
}

func WriteFrame(writer io.Writer, frame Frame) error {
	if err := frame.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return ErrInvalidMessage
	}
	if len(encoded) > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	encoded = append(encoded, '\n')
	for len(encoded) > 0 {
		n, writeErr := writer.Write(encoded)
		if writeErr != nil {
			return writeErr
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		encoded = encoded[n:]
	}
	return nil
}
