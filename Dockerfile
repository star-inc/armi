FROM golang:alpine AS builder
COPY . /workplace
WORKDIR /workplace
RUN apk add --no-cache make
RUN make && make clean-deps

FROM alpine:latest
ENV GIN_MODE release
COPY --from=builder /workplace/build/armi /workplace/armi
WORKDIR /workplace
ENTRYPOINT /workplace/armi
EXPOSE 8000
