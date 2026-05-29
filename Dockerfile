FROM golang:alpine AS builder
COPY . /workplace
WORKDIR /workplace
RUN apk add --no-cache build-base sqlite-dev
ENV CGO_ENABLED=1
ENV CGO_CFLAGS="-Du_int8_t=uint8_t -Du_int16_t=uint16_t -Du_int64_t=uint64_t -Wno-incompatible-pointer-types"
RUN make && make clean-deps

FROM alpine:latest
RUN apk add --no-cache ghostscript
ENV GIN_MODE=release
COPY --from=builder /workplace/build/armi /workplace/armi
WORKDIR /workplace
ENTRYPOINT ["/workplace/armi"]
EXPOSE 8000
