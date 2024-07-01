FROM nvcr.io/nvidia/morpheus/morpheus:v24.03.02-runtime


ENV PATH="$PATH:/opt/conda/envs/morpheus/bin"

COPY ./morpheus-env.yaml /tmp/morpheus-env.yaml

RUN mamba install -n morpheus -y -c conda-forge \
        ipywidgets \
        jupyter_contrib_nbextensions \
        notebook=6 &&\
        pip install jupyterlab_nvdashboard==0.9 &&\
        jupyter contrib nbextension install --user


RUN mamba env update -n morpheus -f /tmp/morpheus-env.yaml

RUN git clone https://github.com/NVIDIA/GenerativeAIExamples.git /tmp/GenerativeAIExamples/ && \
     mv /tmp/GenerativeAIExamples/experimental/event-driven-rag-cve-analysis/ /workspace/examples && \
     rm -rf /tmp/GenerativeAIExamples/

CMD ["jupyter-lab", "--ip=0.0.0.0", "--no-browser", "--allow-root"]

