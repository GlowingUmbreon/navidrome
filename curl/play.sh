curl 'http://localhost:4533/rest/jukeboxControl' \
    -H 'Accept: application/json' \
    -G \
    -d 'u=admin' \
    -d 't=8e3812deca38fdfdaf98e669b71475cb' \
    -d 's=d3c18e' \
    -d 'f=json' \
    -d 'v=1.8.0' \
    -d 'c=NavidromeUI' \
    \
    -d 'action=start'