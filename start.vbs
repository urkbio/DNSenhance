Option Explicit

' 获取当前脚本所在目录
Dim FSO, Shell, WMI, DNS_PATH, WORK_DIR
Set FSO = CreateObject("Scripting.FileSystemObject")
Set Shell = CreateObject("WScript.Shell")
Set WMI = GetObject("winmgmts:\\.\root\cimv2")

' 设置程序路径和工作目录
DNS_PATH = FSO.BuildPath(FSO.GetParentFolderName(WScript.ScriptFullName), "DNSenhance.exe")
WORK_DIR = FSO.GetParentFolderName(WScript.ScriptFullName)

' 检查程序是否存在
If Not FSO.FileExists(DNS_PATH) Then
    MsgBox "找不到 DNSenhance.exe", 16, "错误"
    WScript.Quit
End If

' 检查程序是否已经运行
Dim Processes
Set Processes = WMI.ExecQuery("Select * from Win32_Process Where Name = 'DNSenhance.exe'")
If Processes.Count > 0 Then
    MsgBox "DNSenhance 已经在运行中", 64, "提示"
    WScript.Quit
End If

' 以管理员权限启动程序（隐藏窗口）
CreateObject("Shell.Application").ShellExecute DNS_PATH, "", WORK_DIR, "runas", 0

' 等待程序启动
WScript.Sleep 2000

' 检查是否成功启动
Set Processes = WMI.ExecQuery("Select * from Win32_Process Where Name = 'DNSenhance.exe'")
If Processes.Count > 0 Then
    ' 启动成功，不显示消息
Else
    MsgBox "DNSenhance 启动失败", 16, "错误"
End If
  