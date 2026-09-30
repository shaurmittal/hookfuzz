Output demo.gif
Set FontSize 16
Set Width 1200
Set Height 520
Set TypingSpeed 40ms

Hide
Type "(cd sample-app && npm start >/dev/null 2>&1 &); sleep 2; clear"
Enter
Sleep 3s
Show

Type "./hookfuzz run"
Enter
Sleep 5s

Hide
Type "lsof -ti:4242 | xargs kill"
Enter
