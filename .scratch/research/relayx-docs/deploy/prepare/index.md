<!-- Source URL: https://docs.relayx.cc/deploy/prepare | Capture date: 2026-11-10 -->

# 准备概览

需要提前准备好的一些服务和配置，确保部署顺利。

## [准备说明](intro/index.md)

- 一台服务器，配置 1C2G 以上，越高越好。

## [数据库](database/index.md)

在 部署文档 中，我们提供的 compose.yaml 文件，已经自带了 MySQL 和 Redis，你可以不用自己部署。

## [支付服务 (商业版)](payment/index.md)

支付服务仅支持商业版，个人版无法使用。

## [SMTP 服务](smtp/index.md)

SMTP 服务主要用来给用户发送验证码，我们推荐使用 SaaS 服务，因为自建邮件服务需要自己维护，比较麻烦。

## [StackAuth 服务](stackauth/index.md)

我们使用了 StackAuth 服务来管理用户登录注册。

## [Telegram 机器人](telegram/index.md)

Telegram 机器人主要用来发送通知，如充值、订阅等。
