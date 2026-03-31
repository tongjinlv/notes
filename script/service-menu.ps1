#Requires -Version 5.1
# Service menu for notes.exe / LocalNotes. ASCII only to avoid encoding errors.

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
if (Test-Path -LiteralPath (Join-Path $here "notes.exe")) {
    $Root = $here
}
else {
    $Root = Split-Path -Parent $here
}

$Exe = Join-Path $Root "notes.exe"
$Svc = "LocalNotes"

if (-not (Test-Path -LiteralPath $Exe)) {
    Write-Host "ERROR: not found: $Exe"
    exit 1
}

while ($true) {
    Write-Host ""
    Write-Host "=== LocalNotes service ==="
    Write-Host "Exe: $Exe"
    Write-Host "1=install  2=start  3=stop  4=restart  5=uninstall  0=exit"
    $n = Read-Host "Choice"

    if ($n -eq "0") { exit 0 }
    if ($n -eq "1") { & $Exe -service install -svc-name $Svc; continue }
    if ($n -eq "2") { & $Exe -service start -svc-name $Svc; continue }
    if ($n -eq "3") { & $Exe -service stop -svc-name $Svc; continue }
    if ($n -eq "4") { & $Exe -service restart -svc-name $Svc; continue }
    if ($n -eq "5") { & $Exe -service uninstall -svc-name $Svc; continue }
    Write-Host "Invalid choice."
}
