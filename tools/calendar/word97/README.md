# Word97活动正文辅助阅读器

本模块供离线取证导入使用，不是独立验收CLI。直接运行reader.py的退出码不表示任何文件已通过审核；制度结论仍由完整原件身份、核字、固定事实及Go独立审核共同建立。

```python
from tools.calendar.word97.reader import read_main_text

with open("official.doc", "rb") as source:
    raw = source.read(4 * 1024 * 1024 + 1)
text = read_main_text(raw)
```

支持本批遇到的4MiB以内、512字节扇区CFB v3、头内DIFAT、根级WordDocument/0Table或1Table、C1兼容FIB、CLX/PlcPcd活动主文档。按活动CP顺序读取Unicode或纯ASCII压缩片段，不能用扫描物理UTF16字符串代替：快存残留及物理顺序可能不同于正文。FIB的主文档长度与片段表共同限定输出，页眉、脚注等子文档不混入。

这不是通用Word渲染器：不解释隐藏文字、修订显示、域或宏，不保证所有Word版本/编码/排版。扩展DIFAT、加密、未知压缩编码、链循环、范围或结构异常会拒绝。不得绕过拒绝拼接猜测正文，更不能将导出文本哈希当作官方真实性签名。

测试命令为`python -X utf8 -m unittest discover -s tools/calendar/word97 -v`。合成样本把活动文字放在相反物理顺序，并另存未引用旧条款；反例覆盖CFB/miniFAT、FIB/加密、CLX/CP/偏移/字符编码和截断。实际五份更早冻结Word与旧导出主文档仅去空白后逐字相同，证据在本批仓外目录；这不等于视觉渲染或独立官方来源。

规范见微软[CFB](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-cfb/28488197-8193-49d7-84d8-dfd692418ccd)、[FIB读取](https://learn.microsoft.com/en-us/openspecs/office_file_formats/ms-doc/acb08ace-f268-4879-a998-de592103a70e)、[PlcPcd](https://learn.microsoft.com/en-us/openspecs/office_file_formats/ms-doc/1caae71f-35c4-49d7-adf0-af5fc766331c)及[主文档范围](https://learn.microsoft.com/en-us/openspecs/office_file_formats/ms-doc/f426d9a2-004d-418e-8d8c-e7fd88e7c48e)。冻结派生物的版本字段记录实际生成时的生产者SHA；后来仅控制台封装变化不应倒改旧生产者身份，旧代码与等价性记录随证据保留。
