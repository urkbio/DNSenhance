Set WS = CreateObject("WScript.Shell")
programPath = """" & CreateObject("Scripting.FileSystemObject").GetParentFolderName(WScript.ScriptFullName) & "\build\dnsenhance.exe" & """"
WS.Run programPath, 0, False
