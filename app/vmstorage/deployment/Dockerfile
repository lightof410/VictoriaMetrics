ARG base_image=non-existing
FROM $base_image

RUN apk update && apk --update --no-cache add rsync

EXPOSE 8482
EXPOSE 8400
EXPOSE 8401

ENTRYPOINT ["/vmstorage-prod"]
ARG src_binary=non-existing
COPY $src_binary ./vmstorage-prod
