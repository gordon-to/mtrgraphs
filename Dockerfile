FROM golang:1.22-bullseye

MAINTAINER "Adam Gordon"
ARG BUILD_DATE="2024-04-22"
# install mtr and clean up
RUN apt-get update && apt-get install -y mtr && apt-get clean
# Set the Current Working Directory inside the container
WORKDIR /app
COPY go.mod go.sum mtrgraphs.go ./
RUN go mod download
RUN go build -o mtrgraphs .

ENTRYPOINT ["/app/mtrgraphs"]
