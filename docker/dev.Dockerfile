# syntax=docker/dockerfile:1.4
# Use a lightweight Linux distribution as the base image
FROM fedora:latest

# Environment variables and arguments for server port
ARG SERVER_PORT=8080
# Exported server binary and PCK in ./project
ENV GODOT_GAME_BINARY="FracturedExodus.x86_64"
ENV GODOT_GAME_PCK="FracturedExodus.pck"

# Setup Host Gateway IP
ENV HOST_GATEWAY_IP=host.docker.internal

# System dependencies needed by Godot
RUN dnf update -y && dnf install -y wget unzip wayland-devel fontconfig libXcursor openssl openssl-libs libXinerama libXrandr-devel libXi alsa-lib pulseaudio-libs mesa-libGL

# Set working directory and copy the exported server artifacts
WORKDIR /godotapp
COPY project/ ./
RUN chmod +x /godotapp/${GODOT_GAME_BINARY}

# Expose the server port (adjust protocol and port as needed)
EXPOSE ${SERVER_PORT}/udp

# Start the exported server binary
ENTRYPOINT ["/godotapp/FracturedExodus.x86_64", "--headless"]
