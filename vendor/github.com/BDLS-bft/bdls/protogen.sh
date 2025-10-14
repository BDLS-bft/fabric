#!/bin/bash

protoc -I=. -I=vendor --gogofast_out=. message.proto
