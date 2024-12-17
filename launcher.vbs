Option Explicit

Dim Shell, FSO, DNS_PATH, WORK_DIR, WMI, Processes
Dim objShell, strCommand

Set Shell = CreateObject("WScript.Shell")
Set FSO = CreateObject("Scripting.FileSystemObject")
Set WMI = GetObject("winmgmts:\\.\root\cimv2")

' 设置程序路径和工作目录
DNS_PATH = "C:\Users\zyg\Portable\DNSenhance\DNSenhance.exe"
WORK_DIR = "C:\Users\zyg\Portable\DNSenhance"

' 检查程序是否存在
If Not FSO.FileExists(DNS_PATH) Then
    MsgBox "Error: DNSenhance.exe not found at: " & DNS_PATH, 16, "Error"
    WScript.Quit
End If

' 检查程序是否已经运行
Set Processes = WMI.ExecQuery("Select * from Win32_Process Where Name = 'DNSenhance.exe'")
If Processes.Count > 0 Then
    MsgBox "DNSenhance is already running!", 64, "Info"
    WScript.Quit
End If

' 切换工作目录
Shell.CurrentDirectory = WORK_DIR

' 创建启动命令
Set objShell = CreateObject("Shell.Application")
objShell.ShellExecute DNS_PATH, "", WORK_DIR, "runas", 0

' 等待程序启动
WScript.Sleep 3000

' 检查是否成功启动
Set Processes = WMI.ExecQuery("Select * from Win32_Process Where Name = 'DNSenhance.exe'")
If Processes.Count = 0 Then
    MsgBox "Failed to start DNSenhance!", 16, "Error"
End If 