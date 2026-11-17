# Site Uploader

A file manager for Windows, Linux, and macOS that connects to remote servers over SFTP. The app opens its interface in your default browser and runs a local Go server bound to `127.0.0.1`. You need a browser and access to an SFTP server.

| Platform | File in `dist` |
| --- | --- |
| Windows x64 | `SiteUploader.exe` |
| Linux x64 / ARM64 | `SiteUploader-linux-x64` / `SiteUploader-linux-arm64` |
| macOS Apple Silicon / Intel | `SiteUploader-macos-arm64` / `SiteUploader-macos-x64` |

On Windows, open the `.exe` file. On Linux and macOS, make the binary executable with `chmod +x` if needed, then run it from a terminal. For example, on an Apple Silicon Mac, run the following commands.

```sh
chmod +x ./SiteUploader-macos-arm64
./SiteUploader-macos-arm64
```

The macOS builds are unsigned and have not been notarized by Apple. Click **Quit** in the sidebar to close the connection and stop the app. The `-manual` option prints the local URL so you can open it in a browser yourself.

The app interface is currently in Russian. Button names below are translated into English, with the corresponding Russian labels included where helpful.

## Connecting

1. Enter the server IP address or hostname, SSH port (usually `22`), and username.
2. Enter your SSH password or the path to an OpenSSH private key. If the key is encrypted, enter its passphrase in the password field.
3. Enter the remote directory, such as `/var/www/site`. Leave this field empty to open the user's home directory.
4. Click **Connect** (`Подключиться`). On the first connection, verify the SHA256 host key fingerprint with the server administrator. The app remembers the approved key and rejects connections if it changes.

The server must have the SFTP subsystem enabled. Uploading, replacing, moving, and deleting files require the appropriate permissions on the remote directory.

## Site profiles

Click **+** next to **Sites** (`Сайты`), fill in the connection details, and click **Save profile…** (`Сохранить профиль…`). Select a site in the sidebar to load its profile. To update or rename a profile, select it and save it again.

To remember an SSH password or key passphrase, enter it, select **Remember password when saving the profile** (`Запомнить пароль при сохранении профиля`), and save the profile. Passwords are protected using DPAPI on Windows, Keychain on macOS, and Secret Service on Linux. Saving passwords on Linux requires a running Secret Service provider, such as GNOME Keyring. To remove a saved password, clear the checkbox and save the profile again.

Settings are stored in a `Site Uploader` subdirectory of the system configuration directory. Windows uses the existing `%APPDATA%\Site Uploader\settings.json` file, so profiles and passwords saved by earlier versions load automatically under the same Windows account.

## Managing files

- Double-click a directory to open it, or a UTF-8 text file up to 1 MiB to open the editor.
- **Upload** (`Загрузить`) lets you select one or more files. You can also drag files from your file manager into the list.
- **Download** (`Скачать`) saves the selected file to your computer. To replace a remote file, upload a new copy and confirm the replacement.
- **Move / rename** (`Переместить / переименовать`, `F2`) accepts a new name or a destination path including the file or directory name, such as `/var/www/archive/index.html`.
- **Folder** (`Папка`) creates a subdirectory. **Delete** (`Удалить`, `Delete`) asks for confirmation before removing a file, symbolic link, or empty directory. `F5` refreshes the list while focus is in the file interface.

Replacing an existing file or saving changes from the editor requires the server to support the `posix-rename@openssh.com` SFTP extension. This keeps the original file intact until the replacement has finished uploading.

## Building from source

On Windows with Go installed, run these commands from the project directory. The script builds all platform variants into `dist`.

```powershell
go test ./...
powershell -NoProfile -ExecutionPolicy Bypass -File .\build.ps1
```

On Linux and macOS, run `CGO_ENABLED=0 go build -o SiteUploader .` to build for your current platform. The HTML, CSS, and JavaScript interface is embedded in the executable. SFTP support uses [pkg/sftp](https://github.com/pkg/sftp).
