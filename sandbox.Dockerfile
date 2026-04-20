# Sandbox image for executing LLM-generated code.
# The agent runs `docker run --rm --network none -v <tmp>:/work <image> bash /work/run.sh`
# so we need bash plus a small set of common-language toolchains.
FROM alpine:3.20

RUN apk add --no-cache \
    bash \
    coreutils \
    ca-certificates \
    go \
    python3 \
    py3-pip \
    nodejs \
    git \
    make \
    curl

WORKDIR /work
CMD ["bash"]
