# Termux:API & Ecosystem Command Reference

Complete Termux:API command catalog plus related Android/Termux ecosystem integrations (Widget, Services, Activity Manager, Scheduler) for use in **dispatcher-go**.

---

## Overview: Command Categories

| Category | Scope | Primary Package |
|---|---|---|
| **Device & System** | Battery, audio, brightness, volume, WiFi, USB, telephony, notifications | `termux-api` |
| **Location & Sensors** | GPS, cell location, sensor streams | `termux-api` |
| **Telephony & Messaging** | SMS, call logs, contacts | `termux-api` |
| **Media & Audio** | Camera, microphone, TTS, media player | `termux-api` |
| **Storage & File I/O** | Clipboard, SAF (Storage Access Framework), sharing | `termux-api` |
| **App Lifecycle & Launch** | App launch, force-stop, package queries, foreground app | Android `am` + Termux shell |
| **Scheduling & Cron** | Deferred/recurring jobs, system scheduler | `termux-job-scheduler` + `termux-services` |
| **Widgets & Dashboard** | Home screen widget integration, shortcuts | `termux-widget` |

---

## 1. Device & System State

```
termux-info                     # Device info (model, Android version, CPU, RAM, storage)
termux-battery-status           # Battery level, temp, status (charging/discharging)
termux-audio-info               # Audio device info (speakers, mics, headphones connected)
termux-brightness <N>           # Get/set screen brightness (0-255)
termux-volume <stream> [<N>]    # Get/set volume for stream (music, call, alarm, notification, ring)
termux-wifi-connectioninfo      # Current WiFi connection details
termux-wifi-scaninfo            # Available WiFi networks (requires permission request)
termux-wifi-enable              # Enable WiFi
termux-telephony-deviceinfo     # Device phone number, SIM state, carrier
termux-telephony-cellinfo       # Cell tower info, signal strength
termux-usb                      # List USB devices accessible via USB API
termux-fingerprint              # Authenticate via fingerprint/biometric
termux-keystore <subcommand>    # Cryptographic key management
  - list                        #   List stored keys
  - generate <alias> <size>     #   Generate new key
  - delete <alias>              #   Delete key
  - sign <alias> <data>         #   Sign data with key
  - verify <alias> <data> <sig> #   Verify signature
termux-wallpaper [path]         # Get/set home screen wallpaper
termux-torch on|off             # Control flashlight/torch LED
termux-vibrate [<milliseconds>] # Trigger device vibration
termux-toast "<message>"        # Show toast notification (transient)
termux-dialog <type> [options]  # Show system dialogs (blocks on user input)
  - text                        #   Text input dialog
  - confirm                     #   Yes/No confirmation
  - checkbox                    #   Multi-choice checkboxes
  - counter                     #   Number counter
  - date                        #   Date picker
  - radio                       #   Single-choice radio buttons
  - sheet                       #   List/menu sheet
  - spinner                     #   Spinner/dropdown
  - speech                      #   Voice input dialog
  - time                        #   Time picker
```

**Output:** Most commands return JSON to stdout.

---

## 2. Location & Sensors

```
termux-location -p <provider> -r <mode>
  -p gps|network|passive        # GPS, network triangulation, or passive
  -r once                       # Single location read
  -r updates                    # Stream location updates (continuous)
  
termux-sensor -l                # List all available sensors
termux-sensor -s <name> -n <count>
  -s <sensor_name>              # Specific sensor (e.g., "Light", "Accelerometer")
  -n <count>                    # Read <count> samples (omit for infinite stream)
  
termux-nfc                      # NFC read/write operations (interactive, requires NFC hardware)

termux-infrared-frequencies     # List supported IR frequencies
termux-infrared-transmit <freq> # Transmit IR signal at frequency
```

---

## 3. Telephony & Messaging

```
termux-sms-list -t <type>       # List SMS messages
  -t inbox|sent|draft|all       # Filter by folder
  
termux-sms-send -n "<number>" "<message>"
  -n <number>                   # Recipient phone number
  (message passed as positional argument)
  
termux-call-log                 # List incoming/outgoing/missed calls with timestamps

termux-contact-list             # List all contacts with phone numbers/emails
```

**Note:** No inbox listener API in Termux:API itself; you must poll `termux-sms-list` or integrate with Android `BroadcastReceiver` (requires custom APK).

---

## 4. Notifications

```
termux-notification [options]
  -t <title>                    # Notification title
  -c <content>                  # Notification body
  -i <id>                       # Notification ID (for updates/replacement)
  -p <priority>                 # -2 (min) to 2 (max)
  --action "<label>:<command>"  # Action button (custom action on tap)
  --vibrate                     # Vibration pattern
  --sound <path>                # Notification sound
  --led <color>                 # LED color (RGB hex)
  
termux-notification-channel <operation> [options]
  create <name>                 # Create notification channel
  -d <description>              # Channel description
  -i <importance>               # Importance: min, low, default, high, max
  -s <sound>                    # Notification sound URI
  -p <priority>                 # Priority level
  
termux-notification-remove <id> # Remove notification by ID

termux-notification-list        # List active notifications (JSON output)
```

**Note:** `termux-notification-listen` does not exist in Termux:API. To listen for incoming notifications system-wide, you need `NotificationListenerService` (requires custom APK or privileged permission).

---

## 5. Media & Audio

```
termux-media-player <action> [<file>]
  play <file>                   # Play audio file (blocking or async)
  pause                         # Pause playback
  stop                          # Stop playback
  
termux-media-scan <path>        # Trigger media scanner on file/directory

termux-camera-info              # List available cameras and capabilities
termux-camera-photo -o <path>   # Capture photo, save to path

termux-microphone-record [options]
  -f <file>                     # Output file (WAV, M4A)
  -d <duration>                 # Duration in seconds (or omit for manual stop)
  -l <limit_in_mb>              # File size limit
  
termux-tts-engines              # List available TTS engines

termux-tts-speak [options]
  -e <engine>                   # TTS engine (default: system default)
  -l <lang>                     # Language code (e.g., en, es, fr)
  -p <pitch>                    # Pitch multiplier (0.5-2.0)
  -r <rate>                     # Speech rate (0.5-2.0)
  "<text>"                      # Text to speak (pass as positional argument)
  
termux-speech-to-text [options] # Voice input (speech-to-text)
  -l <lang>                     # Language code
  -e <engine>                   # STT engine
```

---

## 6. Storage, Clipboard & Sharing

```
termux-clipboard-get            # Read clipboard content to stdout

termux-clipboard-set "<text>"   # Set clipboard content

termux-storage-get              # Browse and select a file via SAF (Storage Access Framework)
                                # Returns file URI and content on stdout

termux-saf-create               # Create SAF URI for persistent access to directory
  -d <display_name>             # Name to display
  
termux-saf-dirs                 # List all SAF-accessible directories

termux-saf-ls <uri>             # List files in SAF directory
  <uri>                         # SAF URI (from termux-saf-create or user selection)
  
termux-saf-read <uri>           # Read file content from SAF
termux-saf-write <uri>          # Write file content to SAF
termux-saf-rm <uri>             # Delete file in SAF
termux-saf-stat <uri>           # Get file metadata (size, timestamps)

termux-share <file>             # Share file via system share sheet
  -a <action>                   # (optional) Action type

termux-download <url>           # Download file from URL
  -d <dir>                      # Download directory
  -f <filename>                 # Output filename
```

---

## 7. App Lifecycle & Launch

**Note:** These are **Android `am` (Activity Manager)** commands, not Termux:API. Access via Termux shell.

```
am start [options] <package>/<activity>
  -a <action>                   # Intent action (default: android.intent.action.MAIN)
  -c <category>                 # Intent category
  -n <component>                # Explicit component (package/activity)
  -d <data>                     # Data URI
  -e <key> <value>              # Extra string parameter
  --es <key> <value>            # Extra string (variant)
  --ei <key> <N>                # Extra integer
  --ez <key> true|false         # Extra boolean
  -f <flags>                    # Intent flags (e.g., FLAG_ACTIVITY_NEW_TASK=0x10000000)
  
am force-stop <package>         # Force-stop an app (kills process, closes tasks)

pm list packages [options]      # List installed packages
  -3                            # Third-party apps only
  -s                            # System apps only
  
pm dump <package>               # Package metadata and manifest info

dumpsys activity [options]      # Activity stack and window info
  activities                    # Running activities
  services                      # Running services
  windows                       # Window info (foreground app)
  
getprop <key>                   # Read system property (e.g., ro.build.version.release)
```

**Foreground app (requires AccessibilityService or `dumpsys` parsing):**
```
termux:API does NOT provide direct foreground app detection.
Parse `dumpsys window | grep mCurrentFocus` or use accessibility service.
```

---

## 8. Scheduling & Cron

### Termux Job Scheduler

```
termux-job-scheduler [options]
  -j <job_id>                   # Unique job identifier
  -p <period>                   # Repeat period (milliseconds)
  -s                            # (Create/update job)
  -r <job_id>                   # (Remove job)
  
Example:
  termux-job-scheduler -j my_daily_task -p 86400000 -s  # Daily (86400s * 1000ms)
```

**Properties:**
- Jobs persist across reboot
- Deferred execution (will run even if app is closed)
- Period is minimum interval; actual execution depends on system idle state
- No guarantee of exact timing (subject to Android Doze power management)

### Termux:Services

**Package:** `termux-services` (separate from `termux-api`)

Manages Termux services (like `sshd`, `apache2`, `tor`) via `systemctl`-like interface:

```
sv-enable <service>             # Enable service (auto-start)
sv-disable <service>            # Disable service
sv-start <service>              # Start service
sv-stop <service>               # Stop service
sv-restart <service>            # Restart service
sv-status <service>             # Check service status

# Services live in: ~/.termux/tasker/ or $PREFIX/var/service/
```

**Use case:** Run daemon processes (web server, SSH, database) that persist across shell sessions.

### Cron / Periodic Tasks

**Option 1: Termux Job Scheduler (recommended)**
```
# Simple, Android-native, survives reboot
termux-job-scheduler -j sync_every_hour -p 3600000 -s
```

**Option 2: `cron` daemon (if installed)**
```
# Install: pkg install cronie
# Edit crontab: crontab -e
# Standard cron syntax applies

# Example: run script every 5 minutes
*/5 * * * * /path/to/script.sh >> /path/to/logfile
```

**Option 3: Termux:Services + custom script**
```
# Create: ~/.termux/tasker/my_task/run
#!/bin/bash
while true; do
  /path/to/task.sh
  sleep 3600  # 1 hour
done

# Enable: sv-enable my_task
```

---

## 9. Widgets & Home Screen Integration

**Package:** `termux-widget` (separate APK)

Displays Termux scripts in home screen widget shortcuts. Each script in `~/.shortcuts/` gets a button.

```
# File: ~/.shortcuts/my_script.sh
#!/bin/bash
termux-notification -t "Task" -c "Running from widget..."
echo "Widget executed at $(date)"

# Make executable:
chmod +x ~/.shortcuts/my_script.sh
```

**Widget updates:**
```
termux-widget-refresh            # Refresh widget UI (if this command exists)
                                 # NOTE: Verify existence in current termux-widget version
```

**In dispatcher-go:**
- Create scripts in `~/.shortcuts/`
- Widget will enumerate them automatically
- Tapping button executes script in Termux session
- Script can call `termux-*` commands or dispatcher-go actions

---

## 10. Command Output Format & Parsing

Most Termux:API commands output **JSON** to stdout:

```bash
termux-battery-status
# Output:
# {
#   "status": "Charging",
#   "level": 85,
#   "temperature": 34,
#   "health": "Good"
# }

termux-location -p gps -r once
# Output:
# {
#   "latitude": 37.7749,
#   "longitude": -122.4194,
#   "altitude": 10.5,
#   "accuracy": 5.0
# }
```

**Error handling:** Commands return non-zero exit code on failure; check `stderr` for error message.

---

## 11. Permission Requirements

| Command | Permission | Behavior |
|---|---|---|
| `termux-sms-*`, `termux-call-log` | READ_SMS, CALL_LOG | Request at runtime |
| `termux-contact-list` | READ_CONTACTS | Request at runtime |
| `termux-location` | LOCATION (fine/coarse) | Request at runtime |
| `termux-camera-*`, `termux-microphone-*` | CAMERA, RECORD_AUDIO | Request at runtime |
| `termux-wifi-*` | CHANGE_WIFI_STATE, ACCESS_WIFI_STATE | Request at runtime |
| `termux-storage-get`, `termux-saf-*` | READ_EXTERNAL_STORAGE, WRITE_EXTERNAL_STORAGE | Usually granted to Termux app |
| `termux-fingerprint` | USE_FINGERPRINT | Request at runtime |
| `termux-notification-listen` | **Not available** (requires `NotificationListenerService`) | N/A |
| `termux-foreground-app` | **Not available** (accessibility service needed) | N/A |

**Access foreground app:**
- Option 1: Parse `dumpsys window` (requires `DUMP` permission, may fail on restricted devices)
- Option 2: Build custom APK with `AccessibilityService`
- Option 3: Use `getprop` to check intent broadcasts (limited)

---

## 12. Integration with dispatcher-go

### Recommended Command Groups

```
# System monitoring
system.info, battery.status, audio.info, wifi.connectioninfo, telephony.deviceinfo

# User I/O
dialog.show (blocks), notification.post, clipboard.get/set, share.send

# Sensors & location
location.get/watch, sensor.list/read/stream

# App control
am start (app launch), am force-stop, pm list packages

# Scheduled tasks
termux-job-scheduler (one-off), termux-services (daemon), cron (periodic)

# Media
camera.photo, microphone.record, tts.speak, media.play/pause/stop

# Storage
termux-storage-get, termux-saf-* (persistent file access)

# Home screen
termux-widget (desktop shortcuts)
```

### Missing / Limited Capability

| Feature | Status | Workaround |
|---|---|---|
| Incoming SMS listener | ❌ Not in API | Poll `termux-sms-list` or custom APK with `BroadcastReceiver` |
| Incoming call listener | ❌ Not in API | Poll `termux-call-log` or custom APK |
| Foreground app detection | ⚠️ Accessibility only | Build APK with `AccessibilityService` or parse `dumpsys` |
| Notification listener | ❌ Not in API | Custom APK with `NotificationListenerService` |
| UI automation (tap, type, gesture) | ❌ Not in API | Custom APK with `AccessibilityService` |
| App widget info | ⚠️ Limited | `dumpsys appwidget` (requires DUMP permission) |
| Lock screen state | ⚠️ Limited | `dumpsys power \| grep "User activity"` |
| Clipboard listener | ❌ Not in API | Poll `termux-clipboard-get` or custom APK |

---

## 13. Version & Compatibility Notes

- **Termux:API package** — Latest in F-Droid / Play Store
- **Termux:Services** — Separate package, some commands may vary
- **Termux:Widget** — Separate APK, home screen integration only
- **Android `am` / `pm`** — Available on all Android versions; output format varies by API level
- **SAF (Storage Access Framework)** — Android 5.0+ (API 21+)

For dispatcher-go, test command availability on target device and **version-gate** any features that might be missing on older installations.

