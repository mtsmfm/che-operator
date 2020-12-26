FROM ruby:2.7.2

ARG USERNAME=app
ARG USER_UID=1000
ARG USER_GID=$USER_UID

RUN groupadd --gid $USER_GID $USERNAME \
  && useradd --uid $USER_UID --gid $USER_GID -m $USERNAME \
  && apt-get update \
  && apt-get install -y sudo less \
  && echo "$USERNAME ALL=(root) NOPASSWD:ALL" > /etc/sudoers.d/$USERNAME \
  && chmod 0440 /etc/sudoers.d/$USERNAME

RUN curl -L https://golang.org/dl/go1.15.2.linux-amd64.tar.gz | tar -zx && \
  mv go /usr/local

ENV PATH=$PATH:/usr/local/go/bin:/home/$USERNAME/go/bin

RUN mkdir -p /app $BUNDLE_PATH /home/app/.config /home/app/go
RUN chown -R $USERNAME /app $BUNDLE_PATH /home/app

USER $USERNAME

WORKDIR /app
