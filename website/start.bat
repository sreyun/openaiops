@echo off
chcp 65001 >nul
cd /d "%~dp0"
if not exist "data" mkdir data
set WEBSITE_ADMIN_PASSWORD=%WEBSITE_ADMIN_PASSWORD%
if "%WEBSITE_ADMIN_PASSWORD%"=="" set WEBSITE_ADMIN_PASSWORD=aiops2026
echo.
echo  AIOps 营销站 + SQLite 已启动
echo  站点:   http://127.0.0.1:8090/
echo  后台:   http://127.0.0.1:8090/ethan.html
echo  密码:   %WEBSITE_ADMIN_PASSWORD%
echo  数据库: %~dp0data\website.db
echo  按 Ctrl+C 停止
echo.
python serve.py 8090
pause
