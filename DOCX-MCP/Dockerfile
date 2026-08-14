FROM python:3.11-slim

WORKDIR /app

COPY requirements.txt .
# 国内服务器可取消下一行注释使用清华镜像加速
# RUN pip config set global.index-url https://pypi.tuna.tsinghua.edu.cn/simple
RUN pip install --no-cache-dir -r requirements.txt

COPY server.py .

EXPOSE 8000

CMD ["python", "server.py"]
