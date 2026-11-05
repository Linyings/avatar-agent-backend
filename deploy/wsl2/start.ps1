param([string]$Distribution = 'Ubuntu-24.04')
$ErrorActionPreference = 'Stop'
$stateRoot = Join-Path $env:LOCALAPPDATA 'AiCat/AvatarMVP'
New-Item -ItemType Directory -Path $stateRoot -Force | Out-Null
& wsl.exe -d $Distribution -u root -- systemctl start docker
if ($LASTEXITCODE -ne 0) { throw '无法启动 WSL2 Docker' }
$pidPath = Join-Path $stateRoot 'wsl-keepalive.pid'
$running = $false
if (Test-Path $pidPath) {
    $previousPid = Get-Content $pidPath -ErrorAction SilentlyContinue
    if ($previousPid -match '^\d+$') {
        $previous = Get-CimInstance Win32_Process -Filter "ProcessId = $previousPid" -ErrorAction SilentlyContinue
        $running = $previous -and $previous.Name -eq 'wsl.exe' -and $previous.CommandLine -like "*$Distribution*sleep infinity*"
    }
}
if (-not $running) {
    $keeper = Start-Process -FilePath "$env:SystemRoot/System32/wsl.exe" -ArgumentList @('-d', $Distribution, '-u', 'root', '--', 'sleep', 'infinity') -WindowStyle Hidden -PassThru
    Set-Content -Path $pidPath -Value $keeper.Id -Encoding ascii
}
