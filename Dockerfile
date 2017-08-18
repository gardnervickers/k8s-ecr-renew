FROM alpine

ADD main main
ENTRYPOINT ["bin/bash", "main"]
