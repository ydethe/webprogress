# WebProgress

This library lets developpers use a web UI to track a long running task.
Example :

```python
from webprogress import track

for a in track(range(10), host='localhost', port=5000, key='wbk_xxxxxxxxxxx'):
    print(a)

```

# Development tips

Progress bar that pushes the progress to a web service :

https://github.com/tqdm/tqdm/blob/master/tqdm/contrib/slack.py

WebUI that can receive the progresses pushed by a client: 

https://nicegui.io/#event_system
