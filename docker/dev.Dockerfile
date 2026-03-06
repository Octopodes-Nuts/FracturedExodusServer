# syntax=docker/dockerfile:1.4
# Use a lightweight Linux distribution as the base image
FROM fedora:latest

# Environment variables and arguments for Godot version and server port
ARG GODOT_VERSION="4.3"
ARG SERVER_PORT=8080
ENV GODOT_FILE_NAME="Godot_v${GODOT_VERSION}-stable_linux.x86_64"
# Name of the PCK file you want to run on the server
ENV GODOT_GAME_NAME="YourGameServer.pck"

# System dependencies needed by Godot
RUN dnf update -y && dnf install -y wget unzip wayland-devel fontconfig libXcursor openssl openssl-libs libXinerama libXrandr-devel libXi alsa-lib pulseaudio-libs mesa-libGL

# Download Godot, set from environment variables
ADD https://github.com/godotengine/godot/releases/download/${GODOT_VERSION}-stable/${GODOT_FILE_NAME}.zip ./
RUN mkdir -p ~/.cache \
    && mkdir -p ~/.config/godot \
    && unzip ${GODOT_FILE_NAME}.zip \
    && mv ${GODOT_FILE_NAME} /usr/local/bin/godot \
    && rm -f ${GODOT_FILE_NAME}.zip

# Set working directory and copy the project
WORKDIR /godotapp
COPY project project/

# Export the game's PCK file using the headless Godot binary
# This assumes your project is in a 'project' subdirectory relative to the Dockerfile
WORKDIR /godotapp/project
RUN godot --headless --export-pack "Linux/DedicatedServer" /godotapp/${GODOT_GAME_NAME}
WORKDIR /godotapp

# Expose the server port (adjust protocol and port as needed)
EXPOSE ${SERVER_PORT}/udp

# Start the server with the exported PCK
SHELL ["/bin/bash", "-c"]
ENTRYPOINT ["godot", "--headless", "--main-pack", "${GODOT_GAME_NAME}"]
