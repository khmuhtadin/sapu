# Re-record the README demo with vhs (https://github.com/charmbracelet/vhs): vhs assets/demo.tape
# Recorded on a clean macOS runner, so the sizes are small and real.
Output assets/demo.gif
Set Shell "bash"
Set FontSize 22
Set Width 1400
Set Height 820
Set Padding 28
Set Theme "Catppuccin Latte"
Set TypingSpeed 55ms
Hide
Type "export PS1='$ ' && clear" Enter
Show
Type "sapu clean" Sleep 400ms Enter
Sleep 6s
Down Sleep 700ms
Right Sleep 3s
Left Sleep 700ms
Enter Sleep 3s
Type "y" Sleep 4s
Type "q" Sleep 1500ms
Type "sapu undo --last" Sleep 400ms Enter
Sleep 5s
