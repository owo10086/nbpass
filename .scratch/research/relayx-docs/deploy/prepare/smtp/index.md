<!-- Source URL: https://docs.relayx.cc/deploy/prepare/smtp | Capture date: 2026-11-10 -->

# SMTP 服务

SMTP 服务主要用来给用户发送验证码，我们推荐使用 SaaS 服务，因为自建邮件服务需要自己维护，比较麻烦。

<a id="使用-saas-服务"></a>

## 使用 SaaS 服务

推荐使用 [Resend](https://resend.com)，提供了每月 3000 封免费邮件，一般情况下够用了。

使用步骤：

1. 注册账号
2. 创建项目
3. 获取 SMTP 配置

![resend](https://docs.relayx.cc/assets/images/resend-f5758f810b48d342e69f528522ddca71.png)

<a id="使用自建邮件服务"></a>

## 使用自建邮件服务

如果你有动手能力，或者有现成的邮件服务，也可以使用。需要的参数跟 Resend 一样。
