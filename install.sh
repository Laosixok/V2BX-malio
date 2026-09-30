#!/bin/bash

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

cur_dir=$(pwd)

# check root
[[ $EUID -ne 0 ]] && echo -e "${red}错误：${plain} 必须使用root用户运行此脚本！\n" && exit 1

# check os
if [[ -f /etc/redhat-release ]]; then
    release="centos"
elif cat /etc/issue | grep -Eqi "alpine"; then
    release="alpine"
elif cat /etc/issue | grep -Eqi "debian"; then
    release="debian"
elif cat /etc/issue | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /etc/issue | grep -Eqi "centos|red hat|redhat|rocky|alma|oracle linux"; then
    release="centos"
elif cat /proc/version | grep -Eqi "debian"; then
    release="debian"
elif cat /proc/version | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /proc/version | grep -Eqi "centos|red hat|redhat|rocky|alma|oracle linux"; then
    release="centos"
elif cat /proc/version | grep -Eqi "arch"; then
    release="arch"
else
    echo -e "${red}未检测到系统版本，请联系脚本作者！${plain}\n" && exit 1
fi

arch=$(uname -m)

if [[ $arch == "x86_64" || $arch == "x64" || $arch == "amd64" ]]; then
    arch="64"
elif [[ $arch == "aarch64" || $arch == "arm64" ]]; then
    arch="arm64-v8a"
else
    echo "不支持的架构: ${arch}（仅支持 amd64 / arm64）"
    exit 1
fi

echo "架构: ${arch}"

if [ "$(getconf WORD_BIT)" != '32' ] && [ "$(getconf LONG_BIT)" != '64' ] ; then
    echo "本软件不支持 32 位系统(x86)，请使用 64 位系统(x86_64)，如果检测有误，请联系作者"
    exit 2
fi

# os version
if [[ -f /etc/os-release ]]; then
    os_version=$(awk -F'[= ."]' '/VERSION_ID/{print $3}' /etc/os-release)
fi
if [[ -z "$os_version" && -f /etc/lsb-release ]]; then
    os_version=$(awk -F'[= ."]+' '/DISTRIB_RELEASE/{print $2}' /etc/lsb-release)
fi

if [[ x"${release}" == x"centos" ]]; then
    if [[ ${os_version} -le 6 ]]; then
        echo -e "${red}请使用 CentOS 7 或更高版本的系统！${plain}\n" && exit 1
    fi
    if [[ ${os_version} -eq 7 ]]; then
        echo -e "${red}注意： CentOS 7 无法使用hysteria1/2协议！${plain}\n"
    fi
elif [[ x"${release}" == x"ubuntu" ]]; then
    if [[ ${os_version} -lt 16 ]]; then
        echo -e "${red}请使用 Ubuntu 16 或更高版本的系统！${plain}\n" && exit 1
    fi
elif [[ x"${release}" == x"debian" ]]; then
    if [[ ${os_version} -lt 8 ]]; then
        echo -e "${red}请使用 Debian 8 或更高版本的系统！${plain}\n" && exit 1
    fi
fi

install_base() {
    if [[ x"${release}" == x"centos" ]]; then
        yum install epel-release wget curl unzip tar crontabs socat ca-certificates -y
        update-ca-trust force-enable
    elif [[ x"${release}" == x"alpine" ]]; then
        apk add wget curl unzip tar socat ca-certificates
        update-ca-certificates
    elif [[ x"${release}" == x"debian" ]]; then
        apt-get update -y
        apt install wget curl unzip tar cron socat ca-certificates -y
        update-ca-certificates
    elif [[ x"${release}" == x"ubuntu" ]]; then
        apt-get update -y
        apt install wget curl unzip tar cron socat -y
        apt-get install ca-certificates wget -y
        update-ca-certificates
    elif [[ x"${release}" == x"arch" ]]; then
        pacman -Sy
        pacman -S --noconfirm --needed wget curl unzip tar cron socat
        pacman -S --noconfirm --needed ca-certificates wget
    fi
}

# 0: running, 1: not running, 2: not installed
check_status() {
    if [[ ! -f /usr/local/V2bX/V2bX ]]; then
        return 2
    fi
    if [[ x"${release}" == x"alpine" ]]; then
        temp=$(service V2bX status | awk '{print $3}')
        if [[ x"${temp}" == x"started" ]]; then
            return 0
        else
            return 1
        fi
    else
        temp=$(systemctl status V2bX | grep Active | awk '{print $3}' | cut -d "(" -f2 | cut -d ")" -f1)
        if [[ x"${temp}" == x"running" ]]; then
            return 0
        else
            return 1
        fi
    fi
}

install_V2bX() {
    local stage asset url backup="" had_core=false
    stage=$(mktemp -d) || return 1
    trap "rm -rf '$stage'" EXIT
    last_version=${1:-}
    if [[ -z "$last_version" ]]; then
        last_version=$(curl -fsSL "https://api.github.com/repos/Laosixok/V2BX-malio/releases/latest" | sed -nE 's/.*"tag_name": *"([^"]+)".*/\1/p')
    fi
    if [[ ! "$last_version" =~ ^[a-zA-Z0-9._-]+$ ]]; then
        echo "无法获取有效发布版本；现有安装未修改。"
        return 1
    fi
    asset="V2bX-linux-${arch}.zip"
    url="https://github.com/Laosixok/V2BX-malio/releases/download/${last_version}"
    echo "下载 V2bX ${last_version} (${arch})"
    if ! curl -fL --retry 3 "$url/$asset" -o "$stage/$asset" ||
       ! curl -fL --retry 3 "$url/SHA256SUMS" -o "$stage/SHA256SUMS"; then
        echo "下载失败；现有安装未修改。"
        return 1
    fi
    # Only verify the selected archive: the release also contains other architectures.
    if ! (cd "$stage" && awk -v name="$asset" '$2 == name {print}' SHA256SUMS > selected.sha256 &&
          test -s selected.sha256 && sha256sum -c selected.sha256 &&
          unzip -tq "$asset" && unzip -q "$asset" -d unpack); then
        echo "发布包校验失败；现有安装未修改。"
        return 1
    fi
    for file in V2bX V2bX.sh V2bX.service initconfig.sh config.json dns.json route.json custom_inbound.json custom_outbound.json geoip.dat geosite.dat geoip.db geosite.db; do
        if [[ ! -s "$stage/unpack/$file" ]]; then
            echo "发布包缺少 $file；现有安装未修改。"
            return 1
        fi
    done
    chmod +x "$stage/unpack/V2bX"
    if ! "$stage/unpack/V2bX" version; then
        echo "新核心无法在此系统运行；现有安装未修改。"
        return 1
    fi
    # Downloads and validation finish before stopping or replacing the existing core.
    mkdir -p /usr/local/V2bX /etc/V2bX
    if [[ -f /usr/local/V2bX/V2bX ]]; then
        had_core=true
        backup=$(mktemp -d /usr/local/V2bX-backup.XXXXXX) || return 1
        chmod 700 "$backup"
        cp -a /usr/local/V2bX "$backup/core" || return 1
        cp -a /etc/V2bX "$backup/config" || return 1
        cp -p /usr/local/V2bX/V2bX /usr/local/V2bX/V2bX.previous || return 1
        echo "升级备份：$backup"
    fi
    if [[ x"${release}" == x"alpine" ]]; then
        service V2bX stop || true
    else
        systemctl stop V2bX || true
    fi
    rollback_core() {
        if [[ "$had_core" == true ]]; then
            if [[ x"${release}" == x"alpine" ]]; then
                service V2bX stop || true
            else
                systemctl stop V2bX || true
            fi
            cp -p "$backup/core/V2bX" /usr/local/V2bX/V2bX.next &&
                mv -f /usr/local/V2bX/V2bX.next /usr/local/V2bX/V2bX || return 1
            if [[ x"${release}" == x"alpine" ]]; then
                service V2bX start || true
            else
                systemctl start V2bX || true
            fi
            echo "新版本升级失败，已恢复旧核心；备份保留在 $backup。请检查服务状态。"
        fi
    }
    # Rename the binary instead of overwriting an executable still used by `update`.
    mv "$stage/unpack/V2bX" "$stage/V2bX" || return 1
    if ! { cp -a "$stage/unpack/." /usr/local/V2bX/ &&
           cp -p "$stage/V2bX" /usr/local/V2bX/V2bX.next &&
           mv -f /usr/local/V2bX/V2bX.next /usr/local/V2bX/V2bX; }; then
        rollback_core
        return 1
    fi
    cd /usr/local/V2bX/ || return 1
    mkdir /etc/V2bX/ -p

    if [[ x"${release}" == x"alpine" ]]; then
        if [[ ! -f /etc/init.d/V2bX ]]; then
        cat <<EOF > /etc/init.d/V2bX
#!/sbin/openrc-run

name="V2bX"
description="V2bX"

command="/usr/local/V2bX/V2bX"
command_args="server"
command_user="root"

pidfile="/run/V2bX.pid"
command_background="yes"

depend() {
        need net
}
EOF
        fi
        chmod +x /etc/init.d/V2bX
        rc-update add V2bX default
        echo -e "${green}V2bX ${last_version}${plain} 安装完成，已设置开机自启"
    else
        if [[ ! -f /etc/systemd/system/V2bX.service ]]; then
            cp V2bX.service /etc/systemd/system/V2bX.service
        fi
        systemctl daemon-reload
        systemctl stop V2bX
        systemctl enable V2bX
        echo -e "${green}V2bX ${last_version}${plain} 安装完成，已设置开机自启"
    fi

    for file in dns.json route.json custom_outbound.json custom_inbound.json geoip.dat geosite.dat geoip.db geosite.db; do
        if [[ ! -f "/etc/V2bX/$file" ]]; then
            cp "$file" /etc/V2bX/ || return 1
        fi
    done
    if [[ ! -f /etc/V2bX/config.json ]]; then
        cp config.json /etc/V2bX/
        echo -e ""
        echo -e "全新安装，请先参看教程：https://v2bx.v-50.me/，配置必要的内容"
        first_install=true
    else
        if [[ x"${release}" == x"alpine" ]]; then
            service V2bX start
        else
            systemctl start V2bX
        fi
        sleep 2
        local status=0
        check_status || status=$?
        echo -e ""
        if [[ $status == 0 ]]; then
            echo -e "${green}V2bX 重启成功${plain}"
        else
            rollback_core
            return 1
        fi
        first_install=false
    fi

    cp V2bX.sh /usr/bin/V2bX
    chmod +x /usr/bin/V2bX
    if [ ! -L /usr/bin/v2bx ]; then
        ln -s /usr/bin/V2bX /usr/bin/v2bx
        chmod +x /usr/bin/v2bx
    fi
    cd "$cur_dir" || return 1
    rm -rf "$stage"
    trap - EXIT
    echo -e ""
    echo "V2bX 管理脚本使用方法 (兼容使用V2bX执行，大小写不敏感): "
    echo "------------------------------------------"
    echo "V2bX              - 显示管理菜单 (功能更多)"
    echo "V2bX start        - 启动 V2bX"
    echo "V2bX stop         - 停止 V2bX"
    echo "V2bX restart      - 重启 V2bX"
    echo "V2bX status       - 查看 V2bX 状态"
    echo "V2bX enable       - 设置 V2bX 开机自启"
    echo "V2bX disable      - 取消 V2bX 开机自启"
    echo "V2bX log          - 查看 V2bX 日志"
    echo "V2bX x25519       - 生成 x25519 密钥"
    echo "V2bX generate     - 生成 V2bX 配置文件"
    echo "V2bX update       - 更新 V2bX"
    echo "V2bX update x.x.x - 更新 V2bX 指定版本"
    echo "V2bX install      - 安装 V2bX"
    echo "V2bX uninstall    - 卸载 V2bX"
    echo "V2bX version      - 查看 V2bX 版本"
    echo "------------------------------------------"
    # 首次安装询问是否生成配置文件
    if [[ $first_install == true ]]; then
        read -rp "检测到你为第一次安装V2bX,是否自动直接生成配置文件？(y/n): " if_generate
        if [[ $if_generate == [Yy] ]]; then
            source /usr/local/V2bX/initconfig.sh
            generate_config_file
        fi
    fi
}

echo -e "${green}开始安装${plain}"
install_base || exit 1
install_V2bX "${1:-}"
